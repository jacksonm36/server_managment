package ops

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Site logs (nginx access/error and PHP errors) live in
// /srv/sites/<domain>/logs. The agent rotates them itself, once a day or
// when one passes logRotateSize: it copies the log into a dated .gz and
// truncates it, keeping logKeep archives per log. Every file is opened
// through an os.Root on the logs folder, which the site user owns, so a
// symlink planted there cannot make root read or write anything else.
const (
	logRotateSize  = 100 << 20
	logRotateEvery = 24 * time.Hour
	logKeep        = 14
)

var rotatedLogs = []string{"access.log", "error.log", "php-error.log"}

// StartLogRotation rotates site logs shortly after start, then hourly
// (each log is only rotated when due).
func StartLogRotation(ctx context.Context, log *slog.Logger) {
	go func() {
		t := time.NewTimer(2 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			rotateSiteLogs(log, time.Now())
			t.Reset(time.Hour)
		}
	}()
}

func rotateSiteLogs(log *slog.Logger, now time.Time) {
	sites, err := listSites()
	if err != nil {
		return
	}
	for _, s := range sites {
		if err := rotateLogDir(filepath.Join(s.home(), "logs"), now); err != nil {
			log.Warn("rotate site logs", "domain", s.Domain, "err", err)
		}
	}
}

func rotateLogDir(dir string, now time.Time) error {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return nil // no logs folder (or not a real one): nothing to do
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, name := range rotatedLogs {
		if err := rotateLog(root, name, now); err != nil {
			return err
		}
	}
	return nil
}

func rotateLog(root *os.Root, name string, now time.Time) error {
	fi, err := root.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return nil
	}
	lastRot := time.Time{}
	if st, err := root.Lstat("." + name + ".rotated"); err == nil && st.Mode().IsRegular() {
		lastRot = st.ModTime()
	}
	if fi.Size() < logRotateSize && now.Sub(lastRot) < logRotateEvery {
		return nil
	}
	if lastRot.IsZero() && fi.Size() < logRotateSize {
		// First run: start the clock instead of rotating a fresh log.
		return touchStamp(root, "."+name+".rotated", now)
	}
	src, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer src.Close()
	if st, err := src.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil
	}
	archive := name + "." + now.UTC().Format("20060102-150405") + ".gz"
	dst, err := root.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(dst)
	_, cerr := io.Copy(zw, src)
	if err := zw.Close(); cerr == nil {
		cerr = err
	}
	if err := dst.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		_ = root.Remove(archive)
		return cerr
	}
	if err := src.Truncate(0); err != nil {
		return err
	}
	if err := touchStamp(root, "."+name+".rotated", now); err != nil {
		return err
	}
	return pruneArchives(root, name)
}

func touchStamp(root *os.Root, name string, now time.Time) error {
	if st, err := root.Lstat(name); err == nil && !st.Mode().IsRegular() {
		_ = root.Remove(name)
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	return root.Chtimes(name, now, now)
}

func pruneArchives(root *os.Root, name string) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := d.Readdirnames(-1)
	d.Close()
	if err != nil {
		return err
	}
	var archives []string
	for _, n := range names {
		if strings.HasPrefix(n, name+".") && strings.HasSuffix(n, ".gz") {
			archives = append(archives, n)
		}
	}
	slices.Sort(archives) // the timestamp sorts oldest first
	for len(archives) > logKeep {
		_ = root.Remove(archives[0])
		archives = archives[1:]
	}
	return nil
}
