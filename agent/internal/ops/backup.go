package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Backups live in backupRoot/<domain>/<domain>-<UTC timestamp>.tar.gz and
// contain the site home (without logs/ and tmp/) under "files/" plus one
// "databases/<name>.sql" dump per selected database.
var backupRoot = "/var/backups/servermgr" // var for tests

type BackupPolicy struct {
	Enabled   bool     `json:"enabled"`
	Every     string   `json:"every"` // daily | weekly
	Hour      int      `json:"hour"`  // local hour to run at
	Keep      int      `json:"keep"`
	Databases []string `json:"databases"`
	LastRun   int64    `json:"last_run,omitempty"`
	LastError string   `json:"last_error,omitempty"`
}

type backupFile struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Created int64  `json:"created"`
}

var backupNameRe = regexp.MustCompile(`^[a-z0-9.-]+-\d{8}-\d{6}\.tar\.gz$`)

func backupDir(domain string) string { return filepath.Join(backupRoot, domain) }

func listBackups(domain string) []backupFile {
	ents, _ := os.ReadDir(backupDir(domain))
	out := []backupFile{}
	for _, e := range ents {
		if !backupNameRe.MatchString(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, backupFile{Name: e.Name(), Size: fi.Size(), Created: fi.ModTime().Unix()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

func backupStatus() (any, error) {
	sites, err := listSites()
	if err != nil {
		return nil, err
	}
	type siteBackups struct {
		Domain  string        `json:"domain"`
		Policy  *BackupPolicy `json:"policy"`
		Backups []backupFile  `json:"backups"`
	}
	out := []siteBackups{}
	for _, s := range sites {
		pol := s.Backup
		if pol == nil {
			pol = &BackupPolicy{Every: "daily", Hour: 3, Keep: 7, Databases: []string{}}
		}
		out = append(out, siteBackups{Domain: s.Domain, Policy: pol, Backups: listBackups(s.Domain)})
	}
	var databases []string
	if _, err := exec.LookPath("mariadb"); err == nil {
		if r, err := mariadb(context.Background(), "SHOW DATABASES;"); err == nil {
			for _, row := range rows(r) {
				if !systemDBs[row[0]] {
					databases = append(databases, row[0])
				}
			}
		}
	}
	free := int64(0)
	var st syscall.Statfs_t
	if err := os.MkdirAll(backupRoot, 0o700); err == nil && syscall.Statfs(backupRoot, &st) == nil {
		free = int64(st.Bavail) * int64(st.Bsize)
	}
	return map[string]any{"sites": out, "databases": databases, "free_bytes": free, "root": backupRoot}, nil
}

// createBackup writes a new archive for s and applies retention.
func createBackup(ctx context.Context, s *Site, databases []string, keep int, logf func(string, ...any)) (string, error) {
	for _, db := range databases {
		if !dbIdent.MatchString(db) || systemDBs[db] {
			return "", fmt.Errorf("invalid database %q", db)
		}
	}
	dir := backupDir(s.Domain)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.tar.gz", s.Domain, time.Now().UTC().Format("20060102-150405"))
	final := filepath.Join(dir, name)
	tmp := final + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	gz, _ := gzip.NewWriterLevel(f, gzip.DefaultCompression)
	tw := tar.NewWriter(gz)

	logf("archiving %s", s.home())
	home := s.home()
	n := 0
	err = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, _ := filepath.Rel(home, path)
		if rel == "logs" || rel == "tmp" {
			return filepath.SkipDir
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			link, _ = os.Readlink(path)
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			return nil // sockets etc. are skipped
		}
		hdr.Name = filepath.ToSlash(filepath.Join("files", rel))
		if fi.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			if err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("archive files: %w", err)
	}
	logf("%d files archived", n)

	for _, db := range databases {
		logf("dumping database %s", db)
		if err := addDump(ctx, tw, db); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	ok = true
	if fi, err := os.Stat(final); err == nil {
		logf("created %s (%d bytes)", final, fi.Size())
	}
	if all := listBackups(s.Domain); keep > 0 && len(all) > keep {
		for _, old := range all[keep:] {
			logf("retention: removing %s", old.Name)
			_ = os.Remove(filepath.Join(dir, old.Name))
		}
	}
	return name, nil
}

// addDump streams mariadb-dump into the archive via a temp file (tar needs
// the size up front).
func addDump(ctx context.Context, tw *tar.Writer, db string) error {
	tmp, err := os.CreateTemp("", "smdump-*.sql")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	cmd := exec.CommandContext(ctx, "mariadb-dump", "--single-transaction", "--routines", "--triggers", "--events", "--default-character-set=utf8mb4", "-u", "root", db)
	cmd.Stdout = tmp
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mariadb-dump %s: %s", db, strings.TrimSpace(stderr.String()))
	}
	fi, err := tmp.Stat()
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "databases/" + db + ".sql", Mode: 0o600, Size: fi.Size(), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, tmp)
	return err
}

func backupCreate(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	keep := 0
	if s.Backup != nil {
		keep = s.Backup.Keep
	}
	if _, err := createBackup(ctx, s, strings.Fields(strings.ReplaceAll(p["databases"], ",", " ")), keep, logf); err != nil {
		return nil, err
	}
	return backupStatus()
}

func backupDelete(p map[string]string, logf func(string, ...any)) (any, error) {
	domain := strings.ToLower(p["domain"])
	if !validDomain(domain) || !backupNameRe.MatchString(p["file"]) {
		return nil, errors.New("invalid backup")
	}
	if err := os.Remove(filepath.Join(backupDir(domain), p["file"])); err != nil {
		return nil, err
	}
	logf("deleted %s", p["file"])
	return backupStatus()
}

func backupPolicy(p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	pol := &BackupPolicy{Enabled: p["enabled"] == "true", Every: p["every"], Databases: []string{}}
	if pol.Every != "daily" && pol.Every != "weekly" {
		return nil, errors.New("every must be daily or weekly")
	}
	if pol.Hour, err = strconv.Atoi(p["hour"]); err != nil || pol.Hour < 0 || pol.Hour > 23 {
		return nil, errors.New("hour must be 0-23")
	}
	if pol.Keep, err = strconv.Atoi(p["keep"]); err != nil || pol.Keep < 1 || pol.Keep > 365 {
		return nil, errors.New("keep must be 1-365")
	}
	for _, db := range strings.Fields(strings.ReplaceAll(p["databases"], ",", " ")) {
		if !dbIdent.MatchString(db) || systemDBs[db] {
			return nil, fmt.Errorf("invalid database %q", db)
		}
		pol.Databases = append(pol.Databases, db)
	}
	if s.Backup != nil {
		pol.LastRun, pol.LastError = s.Backup.LastRun, s.Backup.LastError
	}
	siteMu.Lock()
	s.Backup = pol
	err = saveSite(s)
	siteMu.Unlock()
	if err != nil {
		return nil, err
	}
	logf("backup policy for %s: enabled=%v %s at %02d:00, keep %d", s.Domain, pol.Enabled, pol.Every, pol.Hour, pol.Keep)
	return backupStatus()
}

// ---- restore ----

// safeJoin joins an archive path under base, refusing anything that escapes
// it, directly or through a symlink created earlier in the same archive.
func safeJoin(base, name string) (string, error) {
	clean := filepath.Clean("/" + name)
	if clean == "/" {
		return base, nil
	}
	full := filepath.Join(base, clean)
	// Resolve the longest existing ancestor; missing directories below it
	// are created later and cannot be links yet.
	existing, rest := filepath.Dir(full), ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = filepath.Dir(existing)
	}
	parent, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	parent = filepath.Join(parent, rest)
	if parent != base && !strings.HasPrefix(parent, base+"/") {
		return "", fmt.Errorf("archive entry %q escapes the restore directory", name)
	}
	return filepath.Join(parent, filepath.Base(full)), nil
}

func backupRestore(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	s, err := loadSite(strings.ToLower(p["domain"]))
	if err != nil {
		return nil, err
	}
	if !backupNameRe.MatchString(p["file"]) {
		return nil, errors.New("invalid backup file")
	}
	restoreFiles, restoreDBs := p["files"] != "false", p["databases"] == "true"
	f, err := os.Open(filepath.Join(backupDir(s.Domain), p["file"]))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)

	// Files are extracted next to the site first and swapped in at the end,
	// so a failed restore leaves the live site untouched.
	staging, err := os.MkdirTemp(sitesRoot, ".restore-"+s.Domain+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	staging, _ = filepath.EvalSymlinks(staging)
	dumps := map[string]string{}

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case strings.HasPrefix(hdr.Name, "files/") && restoreFiles:
			dst, err := safeJoin(staging, strings.TrimPrefix(hdr.Name, "files/"))
			if err != nil {
				return nil, err
			}
			if err := extractEntry(tr, hdr, dst); err != nil {
				return nil, err
			}
		case strings.HasPrefix(hdr.Name, "databases/") && restoreDBs && hdr.Typeflag == tar.TypeReg:
			db := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "databases/"), ".sql")
			if !dbIdent.MatchString(db) || systemDBs[db] {
				continue
			}
			tmp, err := os.CreateTemp("", "smrestore-*.sql")
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(tmp, tr)
			tmp.Close()
			defer os.Remove(tmp.Name())
			if err != nil {
				return nil, err
			}
			dumps[db] = tmp.Name()
		}
	}

	if restoreFiles {
		// Swap every restored top-level entry (public/, etc.) into place.
		ents, err := os.ReadDir(staging)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			if e.Name() == "logs" || e.Name() == "tmp" {
				continue
			}
			live := filepath.Join(s.home(), e.Name())
			old := live + ".pre-restore"
			_ = os.RemoveAll(old)
			if _, err := os.Lstat(live); err == nil {
				if err := os.Rename(live, old); err != nil {
					return nil, err
				}
			}
			if err := os.Rename(filepath.Join(staging, e.Name()), live); err != nil {
				_ = os.Rename(old, live)
				return nil, err
			}
			_ = os.RemoveAll(old)
			logf("restored %s", live)
		}
	}
	for db, path := range dumps {
		logf("restoring database %s", db)
		if _, err := mariadb(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", db)); err != nil {
			return nil, err
		}
		in, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "mariadb", "--protocol=socket", "-u", "root", db)
		cmd.Stdin = in
		out, err := cmd.CombinedOutput()
		in.Close()
		if err != nil {
			return nil, fmt.Errorf("restore %s: %s", db, strings.TrimSpace(string(out)))
		}
	}
	logf("restore of %s finished", p["file"])
	return backupStatus()
}

func extractEntry(tr *tar.Reader, hdr *tar.Header, dst string) error {
	mode := os.FileMode(hdr.Mode).Perm()
	if hdr.Mode&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(dst, 0o700); err != nil {
			return err
		}
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		if err != nil {
			return err
		}
	case tar.TypeSymlink:
		// Links are recreated as-is; safeJoin stops later entries from
		// being written through them.
		if err := os.Symlink(hdr.Linkname, dst); err != nil {
			return err
		}
		return os.Lchown(dst, hdr.Uid, hdr.Gid)
	default:
		return nil
	}
	if err := os.Chown(dst, hdr.Uid, hdr.Gid); err != nil {
		return err
	}
	if err := os.Chmod(dst, mode); err != nil {
		return err
	}
	return os.Chtimes(dst, hdr.ModTime, hdr.ModTime)
}

// ---- scheduler ----

// StartBackups runs due scheduled backups; it checks every 10 minutes.
func StartBackups(ctx context.Context, log *slog.Logger) {
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			runDueBackups(ctx, log, time.Now())
		}
	}()
}

func backupDue(pol *BackupPolicy, now time.Time) bool {
	if pol == nil || !pol.Enabled || now.Hour() < pol.Hour {
		return false
	}
	last := time.Unix(pol.LastRun, 0)
	interval := 24 * time.Hour
	if pol.Every == "weekly" {
		interval = 7 * 24 * time.Hour
	}
	// Due once the scheduled hour has passed today and the last run is
	// older than the interval (minus slack so the time does not drift).
	return pol.LastRun == 0 || now.Sub(last) >= interval-2*time.Hour
}

func runDueBackups(ctx context.Context, log *slog.Logger, now time.Time) {
	sites, err := listSites()
	if err != nil {
		return
	}
	for _, s := range sites {
		if !backupDue(s.Backup, now) {
			continue
		}
		log.Info("scheduled backup", "domain", s.Domain)
		logf := func(f string, a ...any) { log.Debug(fmt.Sprintf(f, a...)) }
		_, err := createBackup(ctx, s, s.Backup.Databases, s.Backup.Keep, logf)
		siteMu.Lock()
		if cur, lerr := loadSite(s.Domain); lerr == nil && cur.Backup != nil {
			cur.Backup.LastRun = now.Unix()
			cur.Backup.LastError = ""
			if err != nil {
				cur.Backup.LastError = err.Error()
			}
			_ = saveSite(cur)
		}
		siteMu.Unlock()
		if err != nil {
			log.Error("scheduled backup failed", "domain", s.Domain, "err", err)
		}
	}
}
