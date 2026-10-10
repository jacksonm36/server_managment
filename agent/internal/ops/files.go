package ops

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"
)

// The file manager is confined to the hosted sites. Every path is resolved
// (symlinks included) and must stay inside filesRoot.
const (
	maxFileRead  = 2 << 20
	maxFileWrite = 2 << 20
)

// filesRoot is a variable so tests can point it at a temp dir.
var filesRoot = sitesRoot

// resolvePath cleans p and resolves symlinks on its longest existing prefix,
// so a link inside a site cannot point the manager at /etc.
func resolvePath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", errors.New("path must be absolute")
	}
	clean := filepath.Clean(p)
	existing, rest := clean, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	full := filepath.Join(real, rest)
	if full != filesRoot && !strings.HasPrefix(full, filesRoot+"/") {
		return "", fmt.Errorf("path is outside %s", filesRoot)
	}
	return full, nil
}

// siteHomeOf returns /srv/sites/<domain> for a path inside a site.
func siteHomeOf(p string) (string, bool) {
	rel, err := filepath.Rel(filesRoot, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.Join(filesRoot, strings.Split(rel, "/")[0]), true
}

// protected paths cannot be deleted or renamed.
func protected(p string) bool {
	if p == filesRoot {
		return true
	}
	home, ok := siteHomeOf(p)
	if !ok {
		return true
	}
	switch strings.TrimPrefix(p, home) {
	case "", "/public", "/logs", "/tmp":
		return true
	}
	return false
}

// ownSite chowns p to the owner of its site home (site user : www-data).
func ownSite(p string) error {
	home, ok := siteHomeOf(p)
	if !ok {
		return nil
	}
	st, err := os.Stat(home)
	if err != nil {
		return err
	}
	sys := st.Sys().(*syscall.Stat_t)
	return os.Lchown(p, int(sys.Uid), int(sys.Gid))
}

type fileEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // file | dir | link | other
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mtime"`
	Owner   string `json:"owner"`
	Target  string `json:"target,omitempty"`
}

var (
	userCacheMu sync.Mutex
	userCache   = map[uint32]string{}
)

func ownerName(uid uint32) string {
	userCacheMu.Lock()
	defer userCacheMu.Unlock()
	if n, ok := userCache[uid]; ok {
		return n
	}
	n := strconv.Itoa(int(uid))
	if u, err := user.LookupId(n); err == nil {
		n = u.Username
	}
	userCache[uid] = n
	return n
}

func entryOf(path string, fi fs.FileInfo) fileEntry {
	e := fileEntry{Name: fi.Name(), Size: fi.Size(), Mode: fmt.Sprintf("%04o", fi.Mode().Perm()), ModTime: fi.ModTime().Unix(), Type: "other"}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		e.Type = "link"
		e.Target, _ = os.Readlink(path)
	case fi.IsDir():
		e.Type = "dir"
	case fi.Mode().IsRegular():
		e.Type = "file"
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		e.Owner = ownerName(st.Uid)
	}
	return e
}

func filesList(p map[string]string) (any, error) {
	path := p["path"]
	if path == "" {
		path = filesRoot
	}
	dir, err := resolvePath(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(ents))
	for _, d := range ents {
		fi, err := d.Info()
		if err != nil {
			continue
		}
		out = append(out, entryOf(filepath.Join(dir, d.Name()), fi))
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Type == "dir") != (out[j].Type == "dir") {
			return out[i].Type == "dir"
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return map[string]any{"path": dir, "root": filesRoot, "entries": out}, nil
}

func filesRead(p map[string]string) (any, error) {
	path, err := resolvePath(p["path"])
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxFileRead {
		return nil, fmt.Errorf("file is larger than %d MiB", maxFileRead>>20)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	res := map[string]any{"path": path, "size": fi.Size(), "mode": fmt.Sprintf("%04o", fi.Mode().Perm()), "mtime": fi.ModTime().Unix()}
	if utf8.Valid(b) && !strings.ContainsRune(string(b), 0) {
		res["text"] = string(b)
	} else {
		res["base64"] = base64.StdEncoding.EncodeToString(b)
	}
	return res, nil
}

func filesWrite(p map[string]string, logf func(string, ...any)) (any, error) {
	path, err := resolvePath(p["path"])
	if err != nil {
		return nil, err
	}
	if _, ok := siteHomeOf(path); !ok || protected(path) {
		return nil, errors.New("files can only be written inside a site")
	}
	var data []byte
	if b64, ok := p["content_b64"]; ok {
		if data, err = base64.StdEncoding.DecodeString(b64); err != nil {
			return nil, errors.New("invalid base64 content")
		}
	} else {
		data = []byte(p["content"])
	}
	if len(data) > maxFileWrite {
		return nil, fmt.Errorf("file is larger than %d MiB", maxFileWrite>>20)
	}
	mode := os.FileMode(0o640)
	fi, statErr := os.Lstat(path)
	switch {
	case statErr == nil && !fi.Mode().IsRegular():
		return nil, errors.New("target exists and is not a regular file")
	case statErr == nil && p["overwrite"] != "true":
		return nil, errors.New("file already exists")
	case statErr == nil:
		mode = fi.Mode().Perm()
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		return nil, errors.New("parent directory does not exist")
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".smtmp")
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := ownSite(tmp); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	logf("wrote %s (%d bytes)", path, len(data))
	return map[string]any{"path": path, "size": len(data)}, nil
}

func filesMkdir(p map[string]string, logf func(string, ...any)) (any, error) {
	path, err := resolvePath(p["path"])
	if err != nil {
		return nil, err
	}
	if _, ok := siteHomeOf(path); !ok || protected(path) {
		return nil, errors.New("folders can only be created inside a site")
	}
	if err := os.Mkdir(path, 0o750); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, os.ModeSetgid|0o750); err != nil {
		return nil, err
	}
	if err := ownSite(path); err != nil {
		return nil, err
	}
	logf("created %s", path)
	return map[string]any{"path": path}, nil
}

func filesDelete(p map[string]string, logf func(string, ...any)) (any, error) {
	// Resolve the parent only: deleting a symlink removes the link itself.
	raw := filepath.Clean(p["path"])
	parent, err := resolvePath(filepath.Dir(raw))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(parent, filepath.Base(raw))
	if protected(path) {
		return nil, errors.New("this path cannot be deleted")
	}
	if _, err := os.Lstat(path); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(path); err != nil {
		return nil, err
	}
	logf("deleted %s", path)
	return map[string]any{"path": path}, nil
}

func filesRename(p map[string]string, logf func(string, ...any)) (any, error) {
	rawFrom := filepath.Clean(p["from"])
	fromParent, err := resolvePath(filepath.Dir(rawFrom))
	if err != nil {
		return nil, err
	}
	from := filepath.Join(fromParent, filepath.Base(rawFrom))
	to, err := resolvePath(p["to"])
	if err != nil {
		return nil, err
	}
	if protected(from) || protected(to) {
		return nil, errors.New("this path cannot be renamed")
	}
	fh, _ := siteHomeOf(from)
	th, _ := siteHomeOf(to)
	if fh != th {
		return nil, errors.New("files cannot be moved between sites")
	}
	if _, err := os.Lstat(to); err == nil {
		return nil, errors.New("destination already exists")
	}
	if err := os.Rename(from, to); err != nil {
		return nil, err
	}
	logf("renamed %s -> %s", from, to)
	return map[string]any{"path": to}, nil
}

func filesChmod(p map[string]string, logf func(string, ...any)) (any, error) {
	path, err := resolvePath(p["path"])
	if err != nil {
		return nil, err
	}
	if _, ok := siteHomeOf(path); !ok {
		return nil, errors.New("permissions can only be changed inside a site")
	}
	m, err := strconv.ParseUint(p["mode"], 8, 32)
	if err != nil || m > 0o777 {
		return nil, errors.New("mode must be octal like 0644")
	}
	if m&0o002 != 0 {
		return nil, errors.New("world-writable permissions are not allowed")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(m)
	if fi.IsDir() {
		mode |= fi.Mode() & os.ModeSetgid
	}
	if err := os.Chmod(path, mode); err != nil {
		return nil, err
	}
	logf("chmod %04o %s", m, path)
	return map[string]any{"path": path}, nil
}
