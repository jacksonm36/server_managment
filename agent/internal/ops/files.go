package ops

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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

// openSite opens the site home that holds the resolved path p as an
// os.Root, and returns p relative to it. Everything the file manager does
// to a file goes through that root, so a symlink swapped in after
// resolvePath checked the path cannot lead outside the site (os.Root
// refuses to leave it, whatever the links say).
func openSite(p string) (*os.Root, string, error) {
	home, ok := siteHomeOf(p)
	if !ok {
		return nil, "", errors.New("path is not inside a site")
	}
	fi, err := os.Lstat(home)
	if err != nil {
		return nil, "", err
	}
	if !fi.IsDir() {
		return nil, "", errors.New("site home is not a directory")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, "", err
	}
	rel, err := filepath.Rel(home, p)
	if err != nil {
		root.Close()
		return nil, "", err
	}
	return root, rel, nil
}

// siteOwner is the uid/gid that owns a site's home (site user : www-data).
func siteOwner(root *os.Root) (int, int, error) {
	st, err := root.Stat(".")
	if err != nil {
		return 0, 0, err
	}
	sys := st.Sys().(*syscall.Stat_t)
	return int(sys.Uid), int(sys.Gid), nil
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

// entryOf describes a directory entry; path is relative to root when root
// is set.
func entryOf(root *os.Root, path string, fi fs.FileInfo) fileEntry {
	e := fileEntry{Name: fi.Name(), Size: fi.Size(), Mode: fmt.Sprintf("%04o", fi.Mode().Perm()), ModTime: fi.ModTime().Unix(), Type: "other"}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		e.Type = "link"
		if root != nil {
			e.Target, _ = root.Readlink(path)
		} else {
			e.Target, _ = os.Readlink(path)
		}
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
	var out []fileEntry
	if dir == filesRoot {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		out = make([]fileEntry, 0, len(ents))
		for _, d := range ents {
			if fi, err := d.Info(); err == nil {
				out = append(out, entryOf(nil, filepath.Join(dir, d.Name()), fi))
			}
		}
	} else {
		root, rel, err := openSite(dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		f, err := root.Open(rel)
		if err != nil {
			return nil, err
		}
		ents, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
			return nil, err
		}
		out = make([]fileEntry, 0, len(ents))
		for _, d := range ents {
			if fi, err := d.Info(); err == nil {
				out = append(out, entryOf(root, filepath.Join(rel, d.Name()), fi))
			}
		}
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
	root, rel, err := openSite(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxFileRead {
		return nil, fmt.Errorf("file is larger than %d MiB", maxFileRead>>20)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileRead+1))
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
	root, rel, err := openSite(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	mode := os.FileMode(0o640)
	fi, statErr := root.Lstat(rel)
	switch {
	case statErr == nil && !fi.Mode().IsRegular():
		return nil, errors.New("target exists and is not a regular file")
	case statErr == nil && p["overwrite"] != "true":
		return nil, errors.New("file already exists")
	case statErr == nil:
		mode = fi.Mode().Perm()
	}
	if _, err := root.Stat(filepath.Dir(rel)); err != nil {
		return nil, errors.New("parent directory does not exist")
	}
	uid, gid, err := siteOwner(root)
	if err != nil {
		return nil, err
	}
	// A random temp name with O_EXCL, so nothing planted in advance (a
	// symlink, a FIFO) is ever opened.
	tmp := filepath.Join(filepath.Dir(rel), "."+filepath.Base(rel)+"."+randHex(8)+".smtmp")
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Chmod(mode)
	}
	if werr == nil {
		werr = f.Chown(uid, gid)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, rel)
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return nil, werr
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
	root, rel, err := openSite(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir(rel, 0o750); err != nil {
		return nil, err
	}
	if err := root.Chmod(rel, os.ModeSetgid|0o750); err != nil {
		return nil, err
	}
	uid, gid, err := siteOwner(root)
	if err != nil {
		return nil, err
	}
	if err := root.Lchown(rel, uid, gid); err != nil {
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
	root, rel, err := openSite(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := root.Lstat(rel); err != nil {
		return nil, err
	}
	if err := root.RemoveAll(rel); err != nil {
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
	root, relFrom, err := openSite(from)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	relTo, err := filepath.Rel(fh, to)
	if err != nil {
		return nil, err
	}
	if _, err := root.Lstat(relTo); err == nil {
		return nil, errors.New("destination already exists")
	}
	if err := root.Rename(relFrom, relTo); err != nil {
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
	root, rel, err := openSite(path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fi, err := root.Stat(rel)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(m)
	if fi.IsDir() {
		mode |= fi.Mode() & os.ModeSetgid
	}
	if err := root.Chmod(rel, mode); err != nil {
		return nil, err
	}
	logf("chmod %04o %s", m, path)
	return map[string]any{"path": path}, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
