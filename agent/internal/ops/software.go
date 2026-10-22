package ops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"

	"github.com/jacksonm36/servermgr/agent/internal/sysinfo"
)

// Software from the panel's catalog on Linux (the "software.*" ops the
// Windows agent also answers). The panel resolves each app to distribution
// packages, a vendor repository with pinned signing-key fingerprints, or
// GitHub release files with their SHA-256; nothing here runs a vendor
// script. Release archives go to /opt/tools/<id>, writable by root only,
// with their programs linked into /usr/local/bin.

type swRepo struct {
	Key          string   `json:"key"`
	Fingerprints []string `json:"fingerprints"`
	Source       string   `json:"source"`
	SourceRHEL   string   `json:"source_rhel"`
	Pin          string   `json:"pin"`
}

type swFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type swApp struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Apt     []string `json:"apt"`
	Dnf     []string `json:"dnf"`
	EPEL    bool     `json:"epel"`
	AptRepo *swRepo  `json:"apt_repo"`
	DnfRepo *swRepo  `json:"dnf_repo"`
	Deb     *swFile  `json:"deb"`
	Rpm     *swFile  `json:"rpm"`
	Archive *swFile  `json:"archive"`
	Bin     []string `json:"bin"`
}

var (
	swID      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)
	swPkg     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9+._-]{0,100}$`)
	swBin     = regexp.MustCompile(`^[A-Za-z0-9*._-]{1,80}(=[A-Za-z0-9._-]{1,40})?$`)
	swSource  = regexp.MustCompile(`^https://[A-Za-z0-9.-]+/[A-Za-z0-9._/~$-]*( [A-Za-z0-9._/-]+)*$`)
	swOSWord  = regexp.MustCompile(`^[a-z0-9.]{1,32}$`)
	swFprints = regexp.MustCompile(`^[0-9A-F]{40}$`)
	swHost    = regexp.MustCompile(`^[a-z0-9.-]{1,100}$`)
)

// Where things go; variables for tests.
var (
	swToolsDir   = "/opt/tools"
	swBinDir     = "/usr/local/bin"
	swAptKeys    = "/etc/apt/keyrings"
	swAptSources = "/etc/apt/sources.list.d"
	swAptPrefs   = "/etc/apt/preferences.d"
	swRpmKeys    = "/etc/pki/rpm-gpg"
	swYumRepos   = "/etc/yum.repos.d"
)

// swProtected may not be removed from the panel: without them the server
// stops booting, networking or answering (and the panel loses it).
var swProtected = regexp.MustCompile(`^(openssh-.*|ssh|sudo|systemd.*|udev|dbus.*|apt|dpkg|dnf.*|rpm.*|yum.*|libc6.*|glibc.*|libc-.*|bash|dash|coreutils|util-linux.*|login|passwd|shadow-utils|` +
	`linux-image.*|linux-generic.*|linux-virtual.*|linux-headers.*|kernel.*|grub.*|shim.*|efibootmgr|initramfs-tools.*|dracut.*|cloud-init|cloud-guest-utils|qemu-guest-agent|` +
	`ca-certificates|openssl|libssl.*|netplan.io|ifupdown.*|network-manager|NetworkManager.*|systemd-networkd|iproute2?|iputils.*|isc-dhcp-client|dhcpcd.*|nftables|iptables.*|` +
	`ufw|firewalld|e2fsprogs|xfsprogs|lvm2|mdadm|zfs.*|libzfs.*|proxmox.*|pve-.*|python3|python3-minimal|python3\.[0-9]+.*|perl|perl-base|nginx.*|mariadb-server.*|mysql-server.*|servermgr.*)$`)

func swOSVars() (map[string]string, error) {
	rel := sysinfo.OSRelease()
	v := map[string]string{"ID": strings.ToLower(rel["ID"]), "CODENAME": strings.ToLower(rel["VERSION_CODENAME"]),
		"MAJOR": strings.SplitN(rel["VERSION_ID"], ".", 2)[0]}
	if v["CODENAME"] == "" {
		v["CODENAME"] = strings.ToLower(rel["UBUNTU_CODENAME"])
	}
	for k, x := range v {
		if x != "" && !swOSWord.MatchString(x) {
			return nil, fmt.Errorf("unexpected %s %q in /etc/os-release", k, x)
		}
	}
	return v, nil
}

// swExpand fills ${ID}, ${CODENAME} and ${MAJOR}; a placeholder with no
// value is an error rather than a broken source line.
func swExpand(s string, vars map[string]string) (string, error) {
	var bad error
	out := regexp.MustCompile(`\$\{([A-Z]+)\}`).ReplaceAllStringFunc(s, func(m string) string {
		k := m[2 : len(m)-1]
		if vars[k] == "" {
			bad = fmt.Errorf("this distribution has no %s for %s", strings.ToLower(k), s)
		}
		return vars[k]
	})
	return out, bad
}

func swCheck(a *swApp) error {
	if !swID.MatchString(a.ID) {
		return fmt.Errorf("bad app id %q", a.ID)
	}
	for _, p := range append(slices.Clone(a.Apt), a.Dnf...) {
		for _, alt := range strings.Split(p, "|") {
			if !swPkg.MatchString(alt) {
				return fmt.Errorf("bad package name %q", alt)
			}
		}
	}
	for _, b := range a.Bin {
		if !swBin.MatchString(b) {
			return fmt.Errorf("bad program name %q", b)
		}
	}
	for _, r := range []*swRepo{a.AptRepo, a.DnfRepo} {
		if r == nil {
			continue
		}
		if !strings.HasPrefix(r.Key, "https://") || len(r.Fingerprints) == 0 {
			return errors.New("a repository needs an https key and its fingerprints")
		}
		for _, f := range r.Fingerprints {
			if !swFprints.MatchString(f) {
				return fmt.Errorf("bad fingerprint %q", f)
			}
		}
	}
	for _, f := range []*swFile{a.Deb, a.Rpm, a.Archive} {
		if f != nil && (!strings.HasPrefix(f.URL, "https://") || !sha256Hex.MatchString(f.SHA256)) {
			return errors.New("a download needs an https URL and a SHA-256")
		}
	}
	return nil
}

func installSoftware(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	var apps []swApp
	if err := json.Unmarshal([]byte(p["apps"]), &apps); err != nil || len(apps) == 0 {
		return nil, errors.New("apps: a JSON list of catalog entries")
	}
	for i := range apps {
		if err := swCheck(&apps[i]); err != nil {
			return nil, err
		}
	}
	fam := osFamily()
	if fam != famDebian && fam != famRHEL {
		return nil, errors.New("unsupported distribution (Debian/Ubuntu and Fedora/RHEL-likes are supported)")
	}
	vars, err := swOSVars()
	if err != nil {
		return nil, err
	}
	aptMu.Lock()
	defer aptMu.Unlock()
	var failed []string
	updated := false
	for i := range apps {
		a := &apps[i]
		logf("== %s", a.Name)
		if err := swInstall(ctx, a, fam, vars, &updated, logf); err != nil {
			logf("%s: FAILED: %v", a.Name, err)
			failed = append(failed, a.Name)
			continue
		}
		logf("%s: installed", a.Name)
	}
	summary := fmt.Sprintf("%d installed, %d failed", len(apps)-len(failed), len(failed))
	logf("%s", summary)
	if len(failed) > 0 {
		return nil, fmt.Errorf("%s: %s", summary, strings.Join(failed, ", "))
	}
	return map[string]any{"summary": summary}, nil
}

func swInstall(ctx context.Context, a *swApp, fam string, vars map[string]string, updated *bool, logf func(string, ...any)) error {
	did := false
	if fam == famDebian {
		switch {
		case a.Deb != nil:
			if err := swInstallFile(ctx, a.Deb, ".deb", func(f string) error { return aptRun(ctx, logf, "install", f) }); err != nil {
				return err
			}
			did = true
		case len(a.Apt) > 0:
			if a.AptRepo != nil {
				if err := swAptRepo(ctx, a, vars, logf); err != nil {
					swDropRepo(a.ID, logf)
					return err
				}
				*updated = false
			}
			if !*updated {
				if err := aptUpdate(ctx, logf); err != nil {
					if a.AptRepo != nil {
						swDropRepo(a.ID, logf)
						_ = aptUpdate(ctx, logf)
					}
					return err
				}
				*updated = true
			}
			pkgs, err := swPick(ctx, fam, a.Apt)
			if err == nil {
				err = aptRun(ctx, logf, append([]string{"install", "--no-install-recommends"}, pkgs...)...)
			}
			if err != nil {
				if a.AptRepo != nil {
					swDropRepo(a.ID, logf)
					*updated = false
				}
				return err
			}
			did = true
		}
	} else {
		switch {
		case a.Rpm != nil:
			if err := swInstallFile(ctx, a.Rpm, ".rpm", func(f string) error { return dnfRun(ctx, logf, "install", f) }); err != nil {
				return err
			}
			did = true
		case len(a.Dnf) > 0:
			if a.EPEL && vars["ID"] != "fedora" && exec.CommandContext(ctx, "rpm", "-q", "epel-release").Run() != nil {
				if err := dnfRun(ctx, logf, "install", "epel-release"); err != nil {
					return fmt.Errorf("it needs EPEL, which could not be added: %w", err)
				}
			}
			if a.DnfRepo != nil {
				if err := swDnfRepo(ctx, a, vars, logf); err != nil {
					swDropRepo(a.ID, logf)
					return err
				}
			}
			pkgs, err := swPick(ctx, fam, a.Dnf)
			if err == nil {
				err = dnfRun(ctx, logf, append([]string{"install"}, pkgs...)...)
			}
			if err != nil {
				if a.DnfRepo != nil {
					swDropRepo(a.ID, logf)
				}
				return err
			}
			did = true
		}
	}
	if a.Archive != nil {
		if err := swInstallArchive(ctx, a, logf); err != nil {
			return err
		}
		did = true
	}
	if !did {
		return errors.New("not available for this distribution")
	}
	return nil
}

// swPick resolves "a|b" to the first package the distribution offers.
func swPick(ctx context.Context, fam string, list []string) ([]string, error) {
	var out []string
	for _, p := range list {
		alts := strings.Split(p, "|")
		if len(alts) == 1 {
			out = append(out, p)
			continue
		}
		found := ""
		for _, alt := range alts {
			var err error
			if fam == famDebian {
				var b []byte
				b, err = exec.CommandContext(ctx, "apt-cache", "policy", alt).Output()
				if err == nil && (!bytes.Contains(b, []byte("Candidate:")) || bytes.Contains(b, []byte("Candidate: (none)"))) {
					err = errors.New("none")
				}
			} else {
				err = exec.CommandContext(ctx, "dnf", "-q", "info", alt).Run()
			}
			if err == nil {
				found = alt
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("none of %s is available", strings.ReplaceAll(p, "|", ", "))
		}
		out = append(out, found)
	}
	return out, nil
}

func swInstallFile(ctx context.Context, f *swFile, ext string, install func(string) error) error {
	tmp, err := download(ctx, f.URL, f.SHA256)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	// apt and dnf take a local package by its path and extension.
	named := tmp + ext
	if err := os.Rename(tmp, named); err != nil {
		return err
	}
	defer os.Remove(named)
	if err := os.Chmod(named, 0o644); err != nil {
		return err
	}
	return install(named)
}

// swKey fetches a repository key and returns it ASCII-armoured, only if
// every primary key in it has one of the pinned fingerprints.
func swKey(ctx context.Context, url string, pins []string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var el openpgp.EntityList
	const begin = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	if bytes.Contains(b, []byte(begin)) {
		// Every armoured block in the file, not only the first.
		for _, part := range strings.Split(string(b), begin)[1:] {
			keys, err := openpgp.ReadArmoredKeyRing(strings.NewReader(begin + part))
			if err != nil {
				return nil, fmt.Errorf("signing key %s: %w", url, err)
			}
			el = append(el, keys...)
		}
	} else if el, err = openpgp.ReadKeyRing(bytes.NewReader(b)); err != nil {
		return nil, fmt.Errorf("signing key %s: %w", url, err)
	}
	if len(el) == 0 {
		return nil, fmt.Errorf("signing key %s holds no key", url)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		return nil, err
	}
	for _, e := range el {
		fp := fmt.Sprintf("%X", e.PrimaryKey.Fingerprint)
		if !slices.Contains(pins, fp) {
			return nil, fmt.Errorf("signing key %s holds key %s, which is not the vendor's pinned key; not trusting it", url, fp)
		}
		if err := e.Serialize(w); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return append(buf.Bytes(), '\n'), nil
}

func swAptRepo(ctx context.Context, a *swApp, vars map[string]string, logf func(string, ...any)) error {
	r := a.AptRepo
	keyURL, err := swExpand(r.Key, vars)
	if err != nil {
		return err
	}
	src, err := swExpand(r.Source, vars)
	if err != nil {
		return err
	}
	if !swSource.MatchString(src) {
		return fmt.Errorf("bad repository line %q", src)
	}
	key, err := swKey(ctx, keyURL, r.Fingerprints)
	if err != nil {
		return err
	}
	logf("adding %s's package repository (key %s)", a.Name, strings.Join(r.Fingerprints, ", "))
	keyFile := filepath.Join(swAptKeys, "servermgr-"+a.ID+".asc")
	if err := os.MkdirAll(swAptKeys, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(keyFile, key, 0o644); err != nil {
		return err
	}
	line := fmt.Sprintf("# Added by servermgr for %s\ndeb [arch=amd64 signed-by=%s] %s\n", a.Name, keyFile, src)
	if err := writeFileAtomic(filepath.Join(swAptSources, "servermgr-"+a.ID+".list"), []byte(line), 0o644); err != nil {
		return err
	}
	if r.Pin != "" {
		if !swHost.MatchString(r.Pin) {
			return fmt.Errorf("bad pin %q", r.Pin)
		}
		// Prefer the vendor's packages (Ubuntu's firefox is a snap stub).
		pref := fmt.Sprintf("Package: *\nPin: origin %s\nPin-Priority: 1000\n", r.Pin)
		if err := writeFileAtomic(filepath.Join(swAptPrefs, "servermgr-"+a.ID), []byte(pref), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func swDnfRepo(ctx context.Context, a *swApp, vars map[string]string, logf func(string, ...any)) error {
	r := a.DnfRepo
	src := r.Source
	if r.SourceRHEL != "" && vars["ID"] != "fedora" {
		src = r.SourceRHEL
	}
	src, err := swExpand(src, vars)
	if err != nil {
		return err
	}
	keyURL, err := swExpand(r.Key, vars)
	if err != nil {
		return err
	}
	if !swSource.MatchString(src) || strings.Contains(src, " ") {
		return fmt.Errorf("bad repository URL %q", src)
	}
	key, err := swKey(ctx, keyURL, r.Fingerprints)
	if err != nil {
		return err
	}
	logf("adding %s's package repository (key %s)", a.Name, strings.Join(r.Fingerprints, ", "))
	keyFile := filepath.Join(swRpmKeys, "servermgr-"+a.ID+".asc")
	if err := os.MkdirAll(swRpmKeys, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(keyFile, key, 0o644); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "rpm", "--import", keyFile).CombinedOutput(); err != nil {
		return fmt.Errorf("rpm --import: %s", strings.TrimSpace(string(out)))
	}
	repo := fmt.Sprintf("# Added by servermgr for %s\n[servermgr-%s]\nname=%s (servermgr)\nbaseurl=%s\nenabled=1\ngpgcheck=1\ngpgkey=file://%s\n",
		a.Name, a.ID, a.Name, src, keyFile)
	return writeFileAtomic(filepath.Join(swYumRepos, "servermgr-"+a.ID+".repo"), []byte(repo), 0o644)
}

// swDropRepo removes the repository files servermgr wrote for an app.
func swDropRepo(id string, logf func(string, ...any)) {
	removed := false
	for _, f := range []string{
		filepath.Join(swAptSources, "servermgr-"+id+".list"), filepath.Join(swAptPrefs, "servermgr-"+id),
		filepath.Join(swAptKeys, "servermgr-"+id+".asc"), filepath.Join(swYumRepos, "servermgr-"+id+".repo"),
	} {
		if os.Remove(f) == nil {
			removed = true
		}
	}
	if removed {
		logf("removed the %s repository again, so it does not break other installs", id)
	}
}

// ---- release archives ----

type swToolInfo struct {
	Name    string   `json:"name"`
	Source  string   `json:"source"`
	Links   []string `json:"links"`
	Install string   `json:"installed"`
}

func swInstallArchive(ctx context.Context, a *swApp, logf func(string, ...any)) error {
	logf("downloading %s", a.Archive.URL)
	tmp, err := download(ctx, a.Archive.URL, a.Archive.SHA256)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.MkdirAll(swToolsDir, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(swToolsDir, ".unpack-"+a.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := swUnpack(tmp, a.Archive.URL, stage); err != nil {
		return fmt.Errorf("unpacking: %w", err)
	}
	// One top folder in the archive (ghidra_11.4_PUBLIC/...): use it.
	src := stage
	if ents, err := os.ReadDir(stage); err == nil && len(ents) == 1 && ents[0].IsDir() {
		src = filepath.Join(stage, ents[0].Name())
	}
	dest := filepath.Join(swToolsDir, a.ID)
	swUnlinkTool(a.ID)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err != nil {
		return err
	}
	if err := os.Chmod(dest, 0o755); err != nil {
		return err
	}
	info := swToolInfo{Name: a.Name, Source: a.Archive.URL, Install: time.Now().UTC().Format(time.RFC3339)}
	for _, b := range a.Bin {
		glob, name, _ := strings.Cut(b, "=")
		target := swFindFile(dest, glob)
		if target == "" {
			return fmt.Errorf("%s is not in the archive", glob)
		}
		if name == "" {
			name = filepath.Base(target)
		}
		if err := os.Chmod(target, 0o755); err != nil {
			return err
		}
		link := filepath.Join(swBinDir, name)
		if cur, err := os.Readlink(link); err == nil && !strings.HasPrefix(cur, swToolsDir+"/") {
			return fmt.Errorf("%s already exists and is not from servermgr", link)
		} else if err != nil {
			if _, statErr := os.Lstat(link); statErr == nil {
				return fmt.Errorf("%s already exists and is not from servermgr", link)
			}
		}
		_ = os.Remove(link)
		if err := os.Symlink(target, link); err != nil {
			return err
		}
		info.Links = append(info.Links, link)
		logf("%s -> %s", link, target)
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	return writeFileAtomic(filepath.Join(dest, ".servermgr.json"), b, 0o644)
}

// swFindFile finds the regular file matching glob with the shortest path.
func swFindFile(root, glob string) string {
	best := ""
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		if ok, _ := filepath.Match(glob, d.Name()); ok && (best == "" || len(p) < len(best)) {
			best = p
		}
		return nil
	})
	return best
}

// swUnlinkTool removes the /usr/local/bin links into a tool's folder.
func swUnlinkTool(id string) {
	ents, _ := os.ReadDir(swBinDir)
	prefix := filepath.Join(swToolsDir, id) + "/"
	for _, e := range ents {
		l := filepath.Join(swBinDir, e.Name())
		if t, err := os.Readlink(l); err == nil && strings.HasPrefix(t, prefix) {
			_ = os.Remove(l)
		}
	}
}

// swUnpack unpacks a .zip or .tar.gz into dst, keeping the executable bit
// and refusing entries (and links) that point outside dst.
func swUnpack(archive, url, dst string) error {
	inside := func(rel string) (string, error) {
		rel = filepath.Clean(filepath.FromSlash(rel))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("unsafe path %q in the archive", rel)
		}
		return filepath.Join(dst, rel), nil
	}
	mode := func(m fs.FileMode) fs.FileMode {
		if m&0o111 != 0 {
			return 0o755
		}
		return 0o644
	}
	link := func(target, linkname string) error {
		if filepath.IsAbs(linkname) {
			return nil // skip: links stay inside the tool
		}
		resolved := filepath.Join(filepath.Dir(target), linkname)
		if rel, err := filepath.Rel(dst, resolved); err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.Symlink(linkname, target)
	}
	write := func(target string, r io.Reader, m fs.FileMode) error {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, m)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(r, 4<<30))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	}
	switch {
	case strings.HasSuffix(url, ".zip"):
		zr, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			target, err := inside(f.Name)
			if err != nil {
				return err
			}
			switch {
			case f.FileInfo().IsDir():
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			case f.Mode()&fs.ModeSymlink != 0:
				rc, err := f.Open()
				if err != nil {
					return err
				}
				b, err := io.ReadAll(io.LimitReader(rc, 4096))
				rc.Close()
				if err != nil {
					return err
				}
				if err := link(target, string(b)); err != nil {
					return err
				}
			default:
				rc, err := f.Open()
				if err != nil {
					return err
				}
				err = write(target, rc, mode(f.Mode()))
				rc.Close()
				if err != nil {
					return err
				}
			}
		}
		return nil
	case strings.HasSuffix(url, ".tar.gz") || strings.HasSuffix(url, ".tgz"):
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if strings.Trim(hdr.Name, "./") == "" {
				continue
			}
			target, err := inside(hdr.Name)
			if err != nil {
				return err
			}
			switch hdr.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			case tar.TypeReg:
				if err := write(target, tr, mode(fs.FileMode(hdr.Mode))); err != nil {
					return err
				}
			case tar.TypeSymlink:
				if err := link(target, hdr.Linkname); err != nil {
					return err
				}
			}
		}
	}
	return errors.New("unsupported archive type")
}

// ---- list and remove ----

type swProgram struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Publisher    string `json:"publisher"`
	SizeKB       int64  `json:"size_kb,omitempty"`
	CanUninstall bool   `json:"can_uninstall"`
}

// listSoftware lists the packages installed on purpose (not as a
// dependency) and the tools under /opt/tools.
func listSoftware(ctx context.Context) (any, error) {
	progs := []swProgram{}
	switch osFamily() {
	case famDebian:
		manual, err := exec.CommandContext(ctx, "apt-mark", "showmanual").Output()
		if err != nil {
			return nil, fmt.Errorf("apt-mark: %w", err)
		}
		want := map[string]bool{}
		for _, n := range strings.Fields(string(manual)) {
			want[strings.SplitN(n, ":", 2)[0]] = true
		}
		out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Package}\t${Version}\t${Maintainer}\t${Installed-Size}\t${db:Status-Abbrev}\t${Priority}\t${Essential}\t${Section}\n").Output()
		if err != nil {
			return nil, fmt.Errorf("dpkg-query: %w", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Split(line, "\t")
			// Images mark the whole base system manual: leave out what the
			// distribution requires and the libraries.
			if len(f) < 8 || !want[f[0]] || !strings.HasPrefix(f[4], "ii") || f[6] == "yes" ||
				f[5] == "required" || f[5] == "important" || swLibSection.MatchString(f[7]) {
				continue
			}
			kb, _ := strconv.ParseInt(f[3], 10, 64)
			progs = append(progs, swProgram{ID: "pkg:" + f[0], Name: f[0], Version: f[1], Publisher: swMaintainer(f[2]), SizeKB: kb, CanUninstall: !swProtected.MatchString(f[0])})
		}
	case famRHEL:
		out, err := exec.CommandContext(ctx, "dnf", "-q", "repoquery", "--userinstalled", "--qf", `%{name}\t%{version}-%{release}\t%{vendor}\t%{size}\n`).Output()
		if err != nil {
			return nil, fmt.Errorf("dnf repoquery: %w", err)
		}
		// What came with the OS (within an hour of basesystem) is the base
		// system, not something installed on purpose.
		// (rpm, not dnf, gives the times as numbers.)
		installed := map[string]int64{}
		if b, err := exec.CommandContext(ctx, "rpm", "-qa", "--qf", "%{NAME}\t%{INSTALLTIME}\n").Output(); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if n, t, ok := strings.Cut(l, "\t"); ok {
					installed[n], _ = strconv.ParseInt(t, 10, 64)
				}
			}
		}
		base := installed["basesystem"]
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Split(line, "\t")
			if len(f) < 4 {
				continue
			}
			if t := installed[f[0]]; base > 0 && t > 0 && t <= base+3600 {
				continue
			}
			b, _ := strconv.ParseInt(f[3], 10, 64)
			progs = append(progs, swProgram{ID: "pkg:" + f[0], Name: f[0], Version: f[1], Publisher: f[2], SizeKB: b / 1024, CanUninstall: !swProtected.MatchString(f[0])})
		}
	}
	ents, _ := os.ReadDir(swToolsDir)
	for _, e := range ents {
		if !e.IsDir() || !swID.MatchString(e.Name()) {
			continue
		}
		var info swToolInfo
		b, err := os.ReadFile(filepath.Join(swToolsDir, e.Name(), ".servermgr.json"))
		if err != nil || json.Unmarshal(b, &info) != nil {
			continue
		}
		progs = append(progs, swProgram{ID: "tool:" + e.Name(), Name: info.Name, Version: filepath.Base(info.Source), Publisher: "/opt/tools/" + e.Name(), CanUninstall: true})
	}
	slices.SortFunc(progs, func(a, b swProgram) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return map[string]any{"programs": progs}, nil
}

func swMaintainer(m string) string {
	if i := strings.Index(m, " <"); i > 0 {
		return m[:i]
	}
	return m
}

var (
	swRemv       = regexp.MustCompile(`(?m)^Remv (\S+)`)
	swLibSection = regexp.MustCompile(`(^|/)(libs|oldlibs|libdevel)$`)
)

func uninstallSoftware(ctx context.Context, p map[string]string, logf func(string, ...any)) (any, error) {
	kind, name, _ := strings.Cut(p["id"], ":")
	switch kind {
	case "tool":
		if !swID.MatchString(name) {
			return nil, errors.New("bad tool")
		}
		dir := filepath.Join(swToolsDir, name)
		if _, err := os.Stat(filepath.Join(dir, ".servermgr.json")); err != nil {
			return nil, errors.New("not a tool servermgr installed")
		}
		swUnlinkTool(name)
		logf("removing %s", dir)
		if err := os.RemoveAll(dir); err != nil {
			return nil, err
		}
	case "pkg":
		if !swPkg.MatchString(name) || swProtected.MatchString(name) {
			return nil, fmt.Errorf("%s cannot be removed from the panel", name)
		}
		aptMu.Lock()
		defer aptMu.Unlock()
		switch osFamily() {
		case famDebian:
			// See what apt would take with it first.
			cmd := exec.CommandContext(ctx, "apt-get", "-s", "remove", name)
			cmd.Env = aptEnv()
			out, err := cmd.CombinedOutput()
			if err != nil {
				return nil, fmt.Errorf("apt-get: %s", strings.TrimSpace(string(out)))
			}
			var gone []string
			for _, m := range swRemv.FindAllStringSubmatch(string(out), -1) {
				gone = append(gone, m[1])
				if swProtected.MatchString(m[1]) {
					return nil, fmt.Errorf("removing %s would also remove %s, which the server needs", name, m[1])
				}
			}
			if len(gone) > 25 {
				return nil, fmt.Errorf("removing %s would remove %d packages; do that in the terminal", name, len(gone))
			}
			if err := aptRun(ctx, logf, "remove", name); err != nil {
				return nil, err
			}
		case famRHEL:
			// Only when nothing else needs it: dnf would remove the dependents.
			if out, err := exec.CommandContext(ctx, "rpm", "-e", "--test", name).CombinedOutput(); err != nil {
				return nil, fmt.Errorf("%s is needed by other packages: %s", name, strings.TrimSpace(string(out)))
			}
			if err := dnfRun(ctx, logf, "remove", name); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("unsupported distribution")
		}
	default:
		return nil, errors.New("bad id")
	}
	return listSoftware(ctx)
}
