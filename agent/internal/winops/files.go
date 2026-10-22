//go:build windows

package winops

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

// The Windows file manager covers the whole machine (the agent runs as
// SYSTEM), except the agent's own folders: its key cannot be read and its
// program and settings cannot be changed from the panel. System folders
// and drive roots cannot be deleted or renamed.

const (
	maxFileRead  = 2 << 20
	maxFileWrite = 2 << 20
)

func envDir(name, def string) string {
	if d := os.Getenv(name); d != "" {
		return filepath.Clean(d)
	}
	return def
}

var (
	winDir      = envDir("SystemRoot", `C:\Windows`)
	programData = envDir("ProgramData", `C:\ProgramData`)
	programs    = envDir("ProgramFiles", `C:\Program Files`)
)

// agentDirs hold the agent's program, key and enrollment files.
func agentDirs() []string {
	return []string{filepath.Join(programs, "servermgr-agent"), filepath.Join(programData, "servermgr-agent"), filepath.Join(winDir, "servermgr")}
}

// systemDirs cannot be deleted or renamed.
func systemDirs() []string {
	return []string{winDir, filepath.Join(winDir, "System32"), filepath.Join(winDir, "SysWOW64"), programs,
		envDir("ProgramFiles(x86)", `C:\Program Files (x86)`), programData, filepath.Join(filepath.VolumeName(winDir)+`\`, "Users")}
}

func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// within reports whether p is dir or inside it.
func within(p, dir string) bool {
	p, dir = strings.ToLower(filepath.Clean(p)), strings.ToLower(filepath.Clean(dir))
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, `\`)+`\`)
}

func inAgentDir(p string) bool {
	for _, d := range agentDirs() {
		if within(p, d) {
			return true
		}
	}
	return false
}

// isDriveRoot is true for "C:\" and the like.
func isDriveRoot(p string) bool {
	v := filepath.VolumeName(p)
	return v != "" && (p == v || p == v+`\`)
}

// protected paths cannot be deleted or renamed.
func protected(p string) bool {
	if isDriveRoot(p) || inAgentDir(p) {
		return true
	}
	for _, d := range systemDirs() {
		if samePath(p, d) {
			return true
		}
	}
	return false
}

// resolvePath cleans p and resolves links on its longest existing prefix,
// so a link cannot point the manager into the agent's folders.
func resolvePath(p string) (string, error) {
	if !filepath.IsAbs(p) || strings.HasPrefix(p, `\\`) {
		return "", errors.New(`path must be absolute, like C:\Users`)
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
	if isDriveRoot(full) && !strings.HasSuffix(full, `\`) {
		full += `\`
	}
	if inAgentDir(full) {
		return "", errors.New("the servermgr agent's own folders cannot be opened from the panel")
	}
	return full, nil
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
	ownerMu    sync.Mutex
	ownerCache = map[string]string{}
)

// ownerOf is the account owning path, like BUILTIN\Administrators.
func ownerOf(path string) string {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return ""
	}
	sid, _, err := sd.Owner()
	if err != nil || sid == nil {
		return ""
	}
	key := sid.String()
	ownerMu.Lock()
	defer ownerMu.Unlock()
	if n, ok := ownerCache[key]; ok {
		return n
	}
	n := key
	if acc, dom, _, err := sid.LookupAccount(""); err == nil {
		n = acc
		if dom != "" {
			n = dom + `\` + acc
		}
	}
	ownerCache[key] = n
	return n
}

// modeOf shows the Windows attributes that matter: read-only, hidden, system.
func modeOf(fi fs.FileInfo) string {
	m := ""
	if d, ok := fi.Sys().(*windows.Win32FileAttributeData); ok {
		a := d.FileAttributes
		for _, f := range []struct {
			bit uint32
			c   string
		}{{windows.FILE_ATTRIBUTE_READONLY, "R"}, {windows.FILE_ATTRIBUTE_HIDDEN, "H"}, {windows.FILE_ATTRIBUTE_SYSTEM, "S"}} {
			if a&f.bit != 0 {
				m += f.c
			}
		}
	}
	return m
}

func entryOf(path string, fi fs.FileInfo) fileEntry {
	e := fileEntry{Name: fi.Name(), Size: fi.Size(), Mode: modeOf(fi), ModTime: fi.ModTime().Unix(), Type: "other", Owner: ownerOf(path)}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		e.Type = "link"
		e.Target, _ = os.Readlink(path)
	case fi.IsDir():
		e.Type = "dir"
	case fi.Mode().IsRegular():
		e.Type = "file"
	}
	return e
}

// drives lists the fixed and removable drives as folders.
func drives() []fileEntry {
	out := []fileEntry{}
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return out
	}
	for i := 0; i < 26; i++ {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		switch windows.GetDriveType(windows.StringToUTF16Ptr(root)) {
		case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_REMOTE:
		default:
			continue
		}
		e := fileEntry{Name: root[:2], Type: "dir"}
		var free, total uint64
		if windows.GetDiskFreeSpaceEx(windows.StringToUTF16Ptr(root), nil, &total, &free) == nil {
			e.Size = int64(total)
		}
		out = append(out, e)
	}
	return out
}

func filesList(p map[string]string) (any, error) {
	if p["path"] == "" {
		return map[string]any{"path": "", "root": "", "entries": drives()}, nil
	}
	dir, err := resolvePath(p["path"])
	if err != nil {
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
	return map[string]any{"path": dir, "root": "", "entries": out}, nil
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
	res := map[string]any{"path": path, "size": fi.Size(), "mode": modeOf(fi), "mtime": fi.ModTime().Unix()}
	if utf8.Valid(b) && !strings.ContainsRune(string(b), 0) {
		res["text"] = string(b)
	} else {
		res["base64"] = base64.StdEncoding.EncodeToString(b)
	}
	return res, nil
}

func filesWrite(p map[string]string, logf logFunc) (any, error) {
	path, err := resolvePath(p["path"])
	if err != nil {
		return nil, err
	}
	if isDriveRoot(path) {
		return nil, errors.New("not a file path")
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
	fi, statErr := os.Lstat(path)
	switch {
	case statErr == nil && !fi.Mode().IsRegular():
		return nil, errors.New("target exists and is not a regular file")
	case statErr == nil && p["overwrite"] != "true":
		return nil, errors.New("file already exists")
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		return nil, errors.New("parent folder does not exist")
	}
	if statErr == nil {
		// Write in place: the file keeps its permissions (ACL) and owner.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return nil, err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	} else if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	logf("wrote %s (%d bytes)", path, len(data))
	return map[string]any{"path": path, "size": len(data)}, nil
}

func filesMkdir(p map[string]string, logf logFunc) (any, error) {
	path, err := resolvePath(p["path"])
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		return nil, err
	}
	logf("created %s", path)
	return map[string]any{"path": path}, nil
}

func filesDelete(p map[string]string, logf logFunc) (any, error) {
	// Resolve the parent only: deleting a link removes the link itself.
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

func filesRename(p map[string]string, logf logFunc) (any, error) {
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
	// A case-only rename is the same file on Windows.
	if _, err := os.Lstat(to); err == nil && !samePath(from, to) {
		return nil, errors.New("destination already exists")
	}
	if err := os.Rename(from, to); err != nil {
		return nil, err
	}
	logf("renamed %s -> %s", from, to)
	return map[string]any{"path": to}, nil
}
