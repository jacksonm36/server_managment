package api

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// ---- the catalog on Linux ----
//
// The same catalog serves Linux servers. An app installs there from, in
// order of preference: the distribution's own signed packages; the
// vendor's package repository, whose signing key must have one of the
// fingerprints pinned here; or a .deb/.rpm or archive from the project's
// GitHub release, checked against the SHA-256 GitHub publishes. Archives
// are unpacked under /opt/tools/<id> (root-owned) with their programs
// linked into /usr/local/bin.

type linuxApp struct {
	Apt  []string // Debian/Ubuntu packages; "a|b" is the first one the distribution has
	Dnf  []string // Fedora/RHEL packages
	EPEL bool     // Dnf needs EPEL on RHEL-likes
	// A vendor repository; when set, Apt/Dnf come from it.
	AptRepo *linuxRepo
	DnfRepo *linuxRepo
	// GitHub release files (Repo, or the app's own Repo).
	Repo    string
	Deb     string // asset pattern of a .deb
	Rpm     string // asset pattern of an .rpm
	Archive string // asset pattern of a .zip or .tar.gz unpacked to /opt/tools/<id>
	// Bin are the archive's programs to put on PATH: a file name (a glob
	// allowed), or glob=name to link it under another name.
	Bin   []string
	Needs []string // installed first on Linux (Java for Ghidra)
	Note  string
}

// linuxRepo is a vendor's package repository. Key, Source and SourceRHEL
// may use ${ID}, ${CODENAME} and ${MAJOR} from the server's os-release;
// a dnf Source may use $basearch.
type linuxRepo struct {
	Key          string   `json:"key"`          // https URL of the signing key
	Fingerprints []string `json:"fingerprints"` // the only primary keys it may hold
	Source       string   `json:"source"`       // apt: "URL suite components"; dnf: baseurl
	SourceRHEL   string   `json:"source_rhel,omitempty"`
	Pin          string   `json:"pin,omitempty"` // apt: prefer this origin over the distribution's packages
}

// linuxFile is a resolved download.
type linuxFile struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// linuxAppJob is one resolved app, as the agent reads it.
type linuxAppJob struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Apt     []string   `json:"apt,omitempty"`
	Dnf     []string   `json:"dnf,omitempty"`
	EPEL    bool       `json:"epel,omitempty"`
	AptRepo *linuxRepo `json:"apt_repo,omitempty"`
	DnfRepo *linuxRepo `json:"dnf_repo,omitempty"`
	Deb     *linuxFile `json:"deb,omitempty"`
	Rpm     *linuxFile `json:"rpm,omitempty"`
	Archive *linuxFile `json:"archive,omitempty"`
	Bin     []string   `json:"bin,omitempty"`
}

// Vendor keys, checked against the fingerprints each vendor publishes.
var (
	keyMozilla = []string{"35BAA0B33E9EB396F59CA838C0BA5CE6DC6315A3"}
	keyGoogle  = []string{"EB4C1BFD4F042F6DDDCCEC917721F63BD38B4796"}
	keyMS      = []string{"BC528686B50D79E339D3721CEB3E94ADBE1229CF"}
	keyAdopt   = []string{"3B04D753C9050D9A5D343F39843C48A565F8F04B"}
	keyTail    = []string{"2596A99EAAB33821893C0A79458CA832957F5868"}
	keyBrave   = []string{"DBF1A116C220B8C7164F98230686B78420038257", "47D32A74E9A9E013A4B4926C68D513D36A73CD96", "B2A3DCA350E67256740DF904DE4EC67BE4B0DCA0"}
)

const (
	appCLI      = "Command line"
	noteDistro  = "The distribution's own version."
	noteDesktop = "A desktop program: it needs a graphical session."
)

// linuxSpecs says how each catalog app installs on Linux; apps without an
// entry are Windows-only.
var linuxSpecs = map[string]*linuxApp{
	"firefox": {Apt: []string{"firefox"}, Dnf: []string{"firefox"}, Note: noteDesktop,
		AptRepo: &linuxRepo{Key: "https://packages.mozilla.org/apt/repo-signing-key.gpg", Fingerprints: keyMozilla,
			Source: "https://packages.mozilla.org/apt mozilla main", Pin: "packages.mozilla.org"}},
	"chrome": {Apt: []string{"google-chrome-stable"}, Dnf: []string{"google-chrome-stable"}, Note: noteDesktop,
		AptRepo: &linuxRepo{Key: "https://dl.google.com/linux/linux_signing_key.pub", Fingerprints: keyGoogle,
			Source: "https://dl.google.com/linux/chrome/deb/ stable main"},
		DnfRepo: &linuxRepo{Key: "https://dl.google.com/linux/linux_signing_key.pub", Fingerprints: keyGoogle,
			Source: "https://dl.google.com/linux/chrome/rpm/stable/$basearch"}},
	"brave": {Apt: []string{"brave-browser"}, Dnf: []string{"brave-browser"}, Note: noteDesktop,
		AptRepo: &linuxRepo{Key: "https://brave-browser-apt-release.s3.brave.com/brave-browser-archive-keyring.gpg", Fingerprints: keyBrave,
			Source: "https://brave-browser-apt-release.s3.brave.com/ stable main"},
		DnfRepo: &linuxRepo{Key: "https://brave-browser-rpm-release.s3.brave.com/brave-core.asc", Fingerprints: keyBrave,
			Source: "https://brave-browser-rpm-release.s3.brave.com/$basearch"}},
	"thunderbird": {Apt: []string{"thunderbird"}, Dnf: []string{"thunderbird"}, Note: noteDistro + " On Ubuntu that is the snap."},
	"vscode": {Apt: []string{"code"}, Dnf: []string{"code"}, Note: noteDesktop,
		AptRepo: &linuxRepo{Key: "https://packages.microsoft.com/keys/microsoft.asc", Fingerprints: keyMS,
			Source: "https://packages.microsoft.com/repos/code stable main"},
		DnfRepo: &linuxRepo{Key: "https://packages.microsoft.com/keys/microsoft.asc", Fingerprints: keyMS,
			Source: "https://packages.microsoft.com/yumrepos/vscode"}},
	"powershell": {Archive: `^powershell-[\d.]+-linux-x64\.tar\.gz$`, Bin: []string{"pwsh"}},
	"git":        {Apt: []string{"git"}, Dnf: []string{"git"}},
	"nodejs":     {Apt: []string{"nodejs", "npm"}, Dnf: []string{"nodejs", "npm"}, Note: noteDistro},
	"temurin": {Apt: []string{"temurin-21-jdk"}, Dnf: []string{"temurin-21-jdk"},
		AptRepo: &linuxRepo{Key: "https://packages.adoptium.net/artifactory/api/gpg/key/public", Fingerprints: keyAdopt,
			Source: "https://packages.adoptium.net/artifactory/deb ${CODENAME} main"},
		DnfRepo: &linuxRepo{Key: "https://packages.adoptium.net/artifactory/api/gpg/key/public", Fingerprints: keyAdopt,
			Source: "https://packages.adoptium.net/artifactory/rpm/fedora/${MAJOR}/$basearch", SourceRHEL: "https://packages.adoptium.net/artifactory/rpm/rhel/${MAJOR}/$basearch"}},
	"dbeaver":   {Repo: "dbeaver/dbeaver", Deb: `^dbeaver-ce-[\d.]+-linux-x86_64\.deb$`, Rpm: `^dbeaver-ce-[\d.]+-linux-x86_64\.rpm$`, Note: noteDesktop},
	"putty":     {Apt: []string{"putty"}, Dnf: []string{"putty"}, EPEL: true},
	"rustdesk":  {Repo: "rustdesk/rustdesk", Deb: `^rustdesk-[\d.]+-x86_64\.deb$`, Rpm: `^rustdesk-[\d.]+-0\.x86_64\.rpm$`, Note: noteDesktop},
	"wireguard": {Apt: []string{"wireguard-tools"}, Dnf: []string{"wireguard-tools"}},
	"tailscale": {Apt: []string{"tailscale"}, Dnf: []string{"tailscale"}, Note: "Run tailscale up once to sign in.",
		AptRepo: &linuxRepo{Key: "https://pkgs.tailscale.com/stable/${ID}/${CODENAME}.noarmor.gpg", Fingerprints: keyTail,
			Source: "https://pkgs.tailscale.com/stable/${ID} ${CODENAME} main"},
		DnfRepo: &linuxRepo{Key: "https://pkgs.tailscale.com/stable/fedora/repo.gpg", Fingerprints: keyTail,
			Source: "https://pkgs.tailscale.com/stable/fedora/$basearch", SourceRHEL: "https://pkgs.tailscale.com/stable/rhel/${MAJOR}/$basearch"}},
	"wireshark": {Apt: []string{"wireshark"}, Dnf: []string{"wireshark"}, Note: "tshark is its command line."},
	"7zip":      {Apt: []string{"7zip|p7zip-full"}, Dnf: []string{"7zip|p7zip"}, EPEL: true},
	"keepassxc": {Apt: []string{"keepassxc"}, Dnf: []string{"keepassxc"}, EPEL: true, Note: noteDesktop},
	"vlc":       {Apt: []string{"vlc"}, Note: noteDesktop + " On Fedora/RHEL it needs RPM Fusion, so Debian/Ubuntu only."},
	"obs":       {Apt: []string{"obs-studio"}, Note: noteDesktop},
	"winmerge":  {Apt: []string{"meld"}, Dnf: []string{"meld"}, Note: "Meld, the Linux counterpart."},
	"python":    {Apt: []string{"python3", "python3-pip", "python3-venv"}, Dnf: []string{"python3", "python3-pip"}},
	"nmap":      {Apt: []string{"nmap"}, Dnf: []string{"nmap"}},
	"zap":       {Archive: `^ZAP_[\d.]+_Linux\.tar\.gz$`, Bin: []string{"zap.sh=zap"}, Needs: []string{"temurin"}},
	"ghidra":    {Archive: `^ghidra_[\d.]+_PUBLIC_\d+\.zip$`, Bin: []string{"ghidraRun=ghidra", "analyzeHeadless"}, Needs: []string{"temurin"}},
	"cyberchef": {Archive: `^CyberChef_v[\d.]+\.zip$`, Note: "Open /opt/tools/cyberchef/CyberChef_v….html in a browser."},
	"yara":      {Apt: []string{"yara"}, Dnf: []string{"yara"}, EPEL: true},
	"capa":      {Archive: `^capa-v[\d.]+-linux\.zip$`, Bin: []string{"capa"}},
	"floss":     {Archive: `^floss-v[\d.]+-linux\.zip$`, Bin: []string{"floss"}},
	"radare2":   {Deb: `^radare2_[\d.]+_amd64\.deb$`, Dnf: []string{"radare2"}, EPEL: true},
	"ilspy":     {Deb: `^ilspy_[\d.]+-1_amd64\.deb$`, Rpm: `^ilspy-[\d.]+-1\.x86_64\.rpm$`, Note: noteDesktop},
	"jadx":      {Archive: `^jadx-[\d.]+\.zip$`, Bin: []string{"jadx", "jadx-gui"}, Needs: []string{"temurin"}},
	"upx":       {Apt: []string{"upx-ucl|upx"}, Dnf: []string{"upx"}, EPEL: true},
	"hayabusa":  {Archive: `^hayabusa-[\d.]+-lin-x64-gnu\.zip$`, Bin: []string{"hayabusa-*-lin-x64-gnu=hayabusa"}},
	"chainsaw":  {Archive: `^chainsaw_x86_64-unknown-linux-gnu\.tar\.gz$`, Bin: []string{"chainsaw"}},

	// Linux only (no Windows entry).
	"htop":      {Apt: []string{"htop"}, Dnf: []string{"htop"}, EPEL: true},
	"btop":      {Apt: []string{"btop"}, Dnf: []string{"btop"}, EPEL: true},
	"tmux":      {Apt: []string{"tmux"}, Dnf: []string{"tmux"}},
	"ncdu":      {Apt: []string{"ncdu"}, Dnf: []string{"ncdu"}, EPEL: true},
	"mtr":       {Apt: []string{"mtr-tiny|mtr"}, Dnf: []string{"mtr"}},
	"tcpdump":   {Apt: []string{"tcpdump"}, Dnf: []string{"tcpdump"}},
	"iperf3":    {Apt: []string{"iperf3"}, Dnf: []string{"iperf3"}},
	"jq":        {Apt: []string{"jq"}, Dnf: []string{"jq"}},
	"lynis":     {Apt: []string{"lynis"}, Dnf: []string{"lynis"}, EPEL: true},
	"clamav":    {Apt: []string{"clamav", "clamav-daemon"}, Dnf: []string{"clamav", "clamav-update"}, EPEL: true},
	"rkhunter":  {Apt: []string{"rkhunter"}, Dnf: []string{"rkhunter"}, EPEL: true},
	"hydra":     {Apt: []string{"hydra"}},
	"john":      {Apt: []string{"john"}, Dnf: []string{"john"}, EPEL: true},
	"hashcat":   {Apt: []string{"hashcat"}},
	"sqlmap":    {Apt: []string{"sqlmap"}},
	"gobuster":  {Apt: []string{"gobuster"}},
	"binwalk":   {Apt: []string{"binwalk"}},
	"sleuthkit": {Apt: []string{"sleuthkit"}, Dnf: []string{"sleuthkit"}, EPEL: true},
}

// linuxOnly are catalog entries with nothing to install on Windows.
var linuxOnly = []winApp{
	{ID: "htop", Name: "htop", Vendor: "htop", Group: appCLI},
	{ID: "btop", Name: "btop", Vendor: "aristocratos", Group: appCLI},
	{ID: "tmux", Name: "tmux", Vendor: "tmux", Group: appCLI},
	{ID: "ncdu", Name: "ncdu (disk usage)", Vendor: "Yorhel", Group: appCLI},
	{ID: "mtr", Name: "mtr", Vendor: "BitWizard", Group: appCLI},
	{ID: "tcpdump", Name: "tcpdump", Vendor: "The Tcpdump Group", Group: appCLI},
	{ID: "iperf3", Name: "iperf3", Vendor: "ESnet", Group: appCLI},
	{ID: "jq", Name: "jq", Vendor: "jq", Group: appCLI},
	{ID: "lynis", Name: "Lynis (security audit)", Vendor: "CISOfy", Group: appPentest},
	{ID: "hydra", Name: "THC Hydra", Vendor: "van Hauser", Group: appPentest},
	{ID: "john", Name: "John the Ripper", Vendor: "Openwall", Group: appPentest},
	{ID: "hashcat", Name: "hashcat", Vendor: "hashcat", Group: appPentest},
	{ID: "sqlmap", Name: "sqlmap", Vendor: "sqlmap", Group: appPentest},
	{ID: "gobuster", Name: "Gobuster", Vendor: "OJ Reeves", Group: appPentest},
	{ID: "clamav", Name: "ClamAV", Vendor: "Cisco Talos", Group: appMalware},
	{ID: "rkhunter", Name: "rkhunter", Vendor: "rkhunter", Group: appMalware},
	{ID: "binwalk", Name: "binwalk", Vendor: "ReFirm Labs", Group: appMalware},
	{ID: "sleuthkit", Name: "The Sleuth Kit", Vendor: "Brian Carrier", Group: appMalware},
}

func init() {
	winApps = append(winApps, linuxOnly...)
	for i := range winApps {
		x := &winApps[i]
		x.Linux = linuxSpecs[x.ID]
		x.OnWindows = x.Kind != ""
		x.OnLinux = x.Linux != nil
		if x.Linux != nil {
			x.LinuxNote = x.Linux.Note
		}
	}
	for id := range linuxSpecs {
		if winAppByID(id) == nil {
			panic("linuxSpecs: no catalog entry " + id)
		}
	}
}

var linuxPkgName = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*(\|[a-z0-9][a-z0-9+._-]*)*$`)

// resolve finds the GitHub files an app installs from on Linux.
func (l *linuxApp) resolve(ctx context.Context, x *winApp) (*linuxAppJob, error) {
	j := &linuxAppJob{ID: x.ID, Name: x.Name, Apt: l.Apt, Dnf: l.Dnf, EPEL: l.EPEL, AptRepo: l.AptRepo, DnfRepo: l.DnfRepo, Bin: l.Bin}
	repo := l.Repo
	if repo == "" {
		repo = x.Repo
	}
	file := func(kind, match string) (*linuxFile, error) {
		if match == "" {
			return nil, nil
		}
		if repo == "" {
			return nil, errors.New("no GitHub repository for its " + kind)
		}
		// Through the Windows resolver and its cache: a GitHub asset with
		// a published SHA-256 (no signer, so the digest is required).
		r, err := appResolveCached(ctx, &winApp{ID: x.ID + "/linux-" + kind, Name: x.Name, Repo: repo, Match: match})
		if err != nil {
			return nil, err
		}
		return &linuxFile{URL: r.URL, SHA256: r.SHA256}, nil
	}
	var err error
	if j.Deb, err = file("deb", l.Deb); err != nil {
		return nil, err
	}
	if j.Rpm, err = file("rpm", l.Rpm); err != nil {
		return nil, err
	}
	if j.Archive, err = file("archive", l.Archive); err != nil {
		return nil, err
	}
	return j, nil
}

// resolveLinuxApps checks ids against the catalog and resolves them for a
// Linux server, each app's dependencies first.
func resolveLinuxApps(ctx context.Context, ids []string) ([]linuxAppJob, error) {
	if len(ids) > len(winApps) {
		return nil, errors.New("too many applications")
	}
	var order []*winApp
	var add func(x *winApp)
	add = func(x *winApp) {
		if slices.Contains(order, x) {
			return
		}
		for _, d := range append(slices.Clone(x.Needs), x.Linux.Needs...) {
			if dx := winAppByID(d); dx != nil && dx.Linux != nil {
				add(dx)
			}
		}
		order = append(order, x)
	}
	for i, id := range ids {
		x := winAppByID(id)
		if x == nil || slices.Contains(ids[:i], id) {
			return nil, fmt.Errorf("unknown application %q", id)
		}
		if x.Linux == nil {
			return nil, fmt.Errorf("%s is for Windows only", x.Name)
		}
		add(x)
	}
	jobs := make([]linuxAppJob, len(order))
	errs := make([]error, len(order))
	var wg sync.WaitGroup
	for i, x := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := x.Linux.resolve(ctx, x)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", x.Name, err)
				return
			}
			jobs[i] = *j
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return jobs, nil
}
