package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Managing a running Windows VM through the QEMU guest agent: its local
// accounts and installing applications. The panel runs fixed PowerShell
// scripts as SYSTEM; values from the request are validated and passed as
// base64 JSON data that the script decodes, never spliced into code.
// Passwords go through Proxmox's set-user-password, never onto a command
// line or into a script.

// ---- running PowerShell through the guest agent ----

// psCommand is powershell.exe running script, encoded so no quoting of
// the script text matters.
func psCommand(script string) []string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", base64.StdEncoding.EncodeToString(b)}
}

// psData is v as a PowerShell expression that yields it as an object: the
// JSON is base64, so nothing in it can end the string or run.
func psData(v any) string {
	b, _ := json.Marshal(v)
	return "([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(b) + "')) | ConvertFrom-Json)"
}

type agentExecStatus struct {
	Exited   int    `json:"exited"`
	ExitCode int    `json:"exitcode"`
	OutData  string `json:"out-data"`
	ErrData  string `json:"err-data"`
}

// agentExec starts command in the guest and returns its pid.
func agentExec(ctx context.Context, cl *proxmox.Client, gp string, command []string) (int, error) {
	var ex struct {
		PID int `json:"pid"`
	}
	if err := cl.Do(ctx, http.MethodPost, gp+"/agent/exec", url.Values{"command": command}, &ex); err != nil {
		return 0, agentErr(err)
	}
	return ex.PID, nil
}

// agentRun runs command in the guest and waits for it; a non-zero exit is
// an error carrying (cleaned) output.
func agentRun(ctx context.Context, cl *proxmox.Client, gp string, command []string) (string, error) {
	pid, err := agentExec(ctx, cl, gp, command)
	if err != nil {
		return "", err
	}
	for {
		var st agentExecStatus
		if err := cl.Do(ctx, http.MethodGet, gp+"/agent/exec-status", url.Values{"pid": {strconv.Itoa(pid)}}, &st); err != nil {
			return "", agentErr(err)
		}
		if st.Exited == 1 {
			if st.ExitCode != 0 {
				msg := cleanText(st.ErrData, 300)
				if msg == "" {
					msg = cleanText(st.OutData, 300)
				}
				return "", fmt.Errorf("exit code %d: %s", st.ExitCode, msg)
			}
			return st.OutData, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(700 * time.Millisecond):
		}
	}
}

func agentErr(err error) error {
	var ae *proxmox.APIError
	if errors.As(err, &ae) && (strings.Contains(strings.ToLower(ae.Msg), "not running") || strings.Contains(strings.ToLower(ae.Msg), "no qga")) {
		return errors.New("the guest agent is not running; start the VM and wait until Windows has booted")
	}
	return err
}

// winGuest resolves a running Windows VM of the request.
func (a *API) winGuest(w http.ResponseWriter, r *http.Request) (*pveScope, string, string, int, bool) {
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return nil, "", "", 0, false
	}
	if typ != "qemu" {
		writeErr(w, http.StatusBadRequest, "only Windows VMs")
		return nil, "", "", 0, false
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return nil, "", "", 0, false
	}
	if !isWindowsConfig(cfg) {
		writeErr(w, http.StatusBadRequest, "this VM's OS type is not Windows")
		return nil, "", "", 0, false
	}
	return s, node, proxmox.GuestPath(node, typ, vmid), vmid, true
}

// ---- local accounts ----

// winUserName is a local account name Windows accepts (20 characters, none
// of " / \ [ ] : ; | = , + * ? < > @).
var winUserName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,19}$`)

type winUser struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Enabled       bool   `json:"enabled"`
	Admin         bool   `json:"admin"`
	Locked        bool   `json:"locked"`
	BuiltIn       bool   `json:"builtin"` // Administrator, Guest and the like
	LastLogon     string `json:"last_logon,omitempty"`
	PasswordSet   string `json:"password_set,omitempty"`
	PasswordNever bool   `json:"password_never_expires"`
}

// winUsersScript lists local accounts as JSON.
const winUsersScript = `$ErrorActionPreference = 'Stop'
$admins = @(Get-LocalGroupMember -SID 'S-1-5-32-544' -ErrorAction SilentlyContinue | ForEach-Object { $_.SID.Value })
$out = @(Get-LocalUser | ForEach-Object {
    $locked = $false
    try { $locked = [bool]([ADSI]('WinNT://./' + $_.Name + ',user')).IsAccountLocked } catch {}
    $rid = [int]($_.SID.Value.Split('-')[-1])
    [pscustomobject]@{
        name = $_.Name; full_name = [string]$_.FullName; enabled = [bool]$_.Enabled; admin = $admins -contains $_.SID.Value
        locked = $locked; builtin = ($rid -lt 1000)
        last_logon = $(if ($_.LastLogon) { $_.LastLogon.ToString('s') } else { '' })
        password_set = $(if ($_.PasswordLastSet) { $_.PasswordLastSet.ToString('s') } else { '' })
        password_never_expires = -not $_.PasswordExpires
    }
})
ConvertTo-Json -InputObject $out -Compress -Depth 3`

func (a *API) handlePVEWinUsers(w http.ResponseWriter, r *http.Request) {
	s, _, gp, _, ok := a.winGuest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := agentRun(ctx, s.Cl, gp, psCommand(winUsersScript))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var users []winUser
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &users); err != nil {
		writeErr(w, http.StatusBadGateway, "could not read the account list from the guest")
		return
	}
	if users == nil {
		users = []winUser{}
	}
	writeJSON(w, http.StatusOK, users)
}

// winUserScript changes one account: $d holds the validated request.
const winUserScript = `$ErrorActionPreference = 'Stop'
$d = %s
$admins = 'S-1-5-32-544'; $users = 'S-1-5-32-545'
switch ($d.op) {
    'create' {
        New-LocalUser -Name $d.name -FullName $d.full_name -Description 'created by servermgr' -NoPassword -AccountNeverExpires | Out-Null
        Set-LocalUser -Name $d.name -PasswordNeverExpires $false
        # -NoPassword also marks the account as not needing one; clear that
        # (UF_PASSWD_NOTREQD) before the agent sets the password.
        $a = [ADSI](WinNT://./ + $d.name + ,user); $a.UserFlags.Value = $a.UserFlags.Value -band (-bnot 0x20); $a.SetInfo()
        Add-LocalGroupMember -SID $users -Member $d.name -ErrorAction SilentlyContinue
        if ($d.admin) { Add-LocalGroupMember -SID $admins -Member $d.name }
    }
    'update' {
        $u = Get-LocalUser -Name $d.name
        if ($null -ne $d.enabled) { if ($d.enabled) { Enable-LocalUser -SID $u.SID } else { Disable-LocalUser -SID $u.SID } }
        if ($null -ne $d.full_name) { Set-LocalUser -SID $u.SID -FullName $d.full_name }
        if ($null -ne $d.admin) {
            $in = @(Get-LocalGroupMember -SID $admins | Where-Object { $_.SID -eq $u.SID }).Count -gt 0
            if ($d.admin -and -not $in) { Add-LocalGroupMember -SID $admins -Member $u.SID }
            if (-not $d.admin -and $in) { Remove-LocalGroupMember -SID $admins -Member $u.SID }
        }
        if ($d.unlock) { $a = [ADSI]('WinNT://./' + $u.Name + ',user'); $a.IsAccountLocked = $false; $a.SetInfo() }
    }
    'delete' {
        $u = Get-LocalUser -Name $d.name
        if ($u.SID.Value -like '*-500') { throw 'the built-in Administrator cannot be deleted' }
        Remove-LocalUser -SID $u.SID
    }
}
'ok'`

func (a *API) winUserChange(w http.ResponseWriter, r *http.Request, data map[string]any, password, audit string) {
	s, _, gp, vmid, ok := a.winGuest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	detail := map[string]any{}
	for k, v := range data {
		detail[k] = v
	}
	fail := func(err error) {
		detail["error"] = err.Error()
		a.audit(r, "", audit, target, detail, false)
		writeErr(w, http.StatusBadGateway, err.Error())
	}
	if _, err := agentRun(ctx, s.Cl, gp, psCommand(fmt.Sprintf(winUserScript, psData(data)))); err != nil {
		fail(err)
		return
	}
	if password != "" {
		if err := s.Cl.Do(ctx, http.MethodPost, gp+"/agent/set-user-password", url.Values{"username": {data["name"].(string)}, "password": {password}}, nil); err != nil {
			fail(fmt.Errorf("setting the password: %w", agentErr(err)))
			return
		}
		detail["password"] = "set"
	}
	a.audit(r, "", audit, target, detail, true)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handlePVEWinUserCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Admin    bool   `json:"admin"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name, req.FullName = strings.TrimSpace(req.Name), strings.TrimSpace(req.FullName)
	if !winUserName.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "account name: up to 20 letters, digits, spaces, . _ -")
		return
	}
	if len(req.FullName) > 64 || strings.ContainsAny(req.FullName, "\r\n\x00") {
		writeErr(w, http.StatusBadRequest, "full name: up to 64 characters")
		return
	}
	if msg := winPasswordProblem(req.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if strings.Contains(strings.ToLower(req.Password), strings.ToLower(req.Name)) {
		writeErr(w, http.StatusBadRequest, "Windows password: must not contain the user name")
		return
	}
	a.winUserChange(w, r, map[string]any{"op": "create", "name": req.Name, "full_name": req.FullName, "admin": req.Admin}, req.Password, "proxmox.windows_user_create")
}

func (a *API) handlePVEWinUserUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled  *bool   `json:"enabled"`
		Admin    *bool   `json:"admin"`
		FullName *string `json:"full_name"`
		Unlock   bool    `json:"unlock"`
		Password string  `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := r.PathValue("user")
	if !winUserName.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad account name")
		return
	}
	data := map[string]any{"op": "update", "name": name, "unlock": req.Unlock}
	if req.Enabled != nil {
		data["enabled"] = *req.Enabled
	}
	if req.Admin != nil {
		data["admin"] = *req.Admin
	}
	if req.FullName != nil {
		f := strings.TrimSpace(*req.FullName)
		if len(f) > 64 || strings.ContainsAny(f, "\r\n\x00") {
			writeErr(w, http.StatusBadRequest, "full name: up to 64 characters")
			return
		}
		data["full_name"] = f
	}
	if req.Password != "" {
		if msg := winPasswordProblem(req.Password); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
	}
	a.winUserChange(w, r, data, req.Password, "proxmox.windows_user_update")
}

func (a *API) handlePVEWinUserDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("user")
	if !winUserName.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "bad account name")
		return
	}
	a.winUserChange(w, r, map[string]any{"op": "delete", "name": name}, "", "proxmox.windows_user_delete")
}

// ---- applications ----

// winApp is an application servermgr can install silently. The download
// is verified in the guest against the publisher's Authenticode signature
// (Signer, a regexp on the certificate subject) and/or a SHA-256 the
// panel looked up from the vendor (GitHub releases publish one).
type winApp struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Vendor string   `json:"vendor"`
	Group  string   `json:"group"` // for the picker: Browsers, Development, …
	Note   string   `json:"note,omitempty"`
	Kind   string   `json:"-"` // msi | exe
	Args   []string `json:"-"`
	Signer string   `json:"-"`
	// Where it comes from: a fixed URL (often the vendor's "latest"
	// redirect), a GitHub repo's latest release asset, or the newest file
	// in a directory listing.
	URL   string `json:"-"`
	Repo  string `json:"-"`
	Dir   string `json:"-"`
	Match string `json:"-"` // asset or file name pattern
}

var winApps = []winApp{
	// Browsers and mail
	{ID: "firefox", Name: "Firefox", Vendor: "Mozilla", Group: "Browsers and mail", Kind: "msi", Signer: `CN="?Mozilla Corporation`,
		URL: "https://download.mozilla.org/?product=firefox-msi-latest-ssl&os=win64&lang=en-US"},
	{ID: "chrome", Name: "Google Chrome", Vendor: "Google", Group: "Browsers and mail", Kind: "msi", Signer: `CN="?Google LLC`,
		URL: "https://dl.google.com/dl/chrome/install/googlechromestandaloneenterprise64.msi"},
	{ID: "brave", Name: "Brave", Vendor: "Brave Software", Group: "Browsers and mail", Kind: "exe", Signer: `CN="?Brave Software`,
		URL: "https://github.com/brave/brave-browser/releases/latest/download/BraveBrowserStandaloneSilentSetup.exe"},
	{ID: "thunderbird", Name: "Thunderbird", Vendor: "Mozilla", Group: "Browsers and mail", Kind: "msi", Signer: `CN="?Mozilla Corporation`,
		URL: "https://download.mozilla.org/?product=thunderbird-msi-latest-ssl&os=win64&lang=en-US"},

	// Development
	{ID: "vscode", Name: "Visual Studio Code", Vendor: "Microsoft", Group: "Development", Kind: "exe", Signer: `CN="?Microsoft Corporation`,
		Args: []string{"/VERYSILENT", "/NORESTART", "/MERGETASKS=!runcode"}, URL: "https://update.code.visualstudio.com/latest/win32-x64/stable"},
	{ID: "powershell", Name: "PowerShell 7", Vendor: "Microsoft", Group: "Development", Kind: "msi", Signer: `CN="?Microsoft Corporation`,
		Args: []string{"ADD_PATH=1"}, Repo: "PowerShell/PowerShell", Match: `^PowerShell-\d+\.\d+\.\d+-win-x64\.msi$`},
	{ID: "git", Name: "Git for Windows", Vendor: "Git for Windows", Group: "Development", Kind: "exe", Signer: `CN="?Johannes Schindelin`,
		Args: []string{"/VERYSILENT", "/NORESTART", "/NOCANCEL", "/SP-"}, Repo: "git-for-windows/git", Match: `^Git-[\d.]+-64-bit\.exe$`},
	{ID: "nodejs", Name: "Node.js 24 LTS", Vendor: "OpenJS Foundation", Group: "Development", Kind: "msi", Signer: `CN="?OpenJS Foundation`,
		Dir: "https://nodejs.org/dist/latest-v24.x/", Match: `^node-v[\d.]+-x64\.msi$`},
	{ID: "temurin", Name: "Java 21 (Eclipse Temurin JDK)", Vendor: "Eclipse Adoptium", Group: "Development", Kind: "msi", Signer: `CN="?Eclipse`,
		Args: []string{"ADDLOCAL=FeatureMain,FeatureEnvironment,FeatureJarFileRunWith,FeatureJavaHome"},
		URL:  "https://api.adoptium.net/v3/installer/latest/21/ga/windows/x64/jdk/hotspot/normal/eclipse"},
	{ID: "dbeaver", Name: "DBeaver Community", Vendor: "DBeaver", Group: "Development", Kind: "exe", Signer: `CN="?DBeaver Corp`,
		Args: []string{"/S", "/allusers"}, URL: "https://dbeaver.io/files/dbeaver-ce-latest-x86_64-setup.exe"},
	{ID: "heidisql", Name: "HeidiSQL", Vendor: "Ansgar Becker", Group: "Development", Kind: "exe",
		Args: []string{"/VERYSILENT", "/NORESTART", "/SUPPRESSMSGBOXES"}, Repo: "HeidiSQL/HeidiSQL", Match: `^HeidiSQL_[\d.]+_Setup\.exe$`},
	{ID: "notepadpp", Name: "Notepad++", Vendor: "Notepad++", Group: "Development", Kind: "exe", Signer: `CN="?NOTEPAD\+\+`,
		Args: []string{"/S"}, Repo: "notepad-plus-plus/notepad-plus-plus", Match: `^npp\.[\d.]+\.Installer\.x64\.exe$`},

	// Remote access and networking
	{ID: "putty", Name: "PuTTY", Vendor: "Simon Tatham", Group: "Remote access and networking", Kind: "msi", Signer: `CN="?Simon Tatham`,
		Dir: "https://the.earth.li/~sgtatham/putty/latest/w64/", Match: `^putty-64bit-[\d.]+-installer\.msi$`},
	{ID: "rustdesk", Name: "RustDesk", Vendor: "RustDesk", Group: "Remote access and networking", Kind: "msi",
		Repo: "rustdesk/rustdesk", Match: `^rustdesk-[\d.]+-x86_64\.msi$`},
	{ID: "wireguard", Name: "WireGuard", Vendor: "WireGuard", Group: "Remote access and networking", Kind: "msi", Signer: `CN="?WireGuard LLC`,
		Args: []string{"DO_NOT_LAUNCH=1"}, Dir: "https://download.wireguard.com/windows-client/", Match: `^wireguard-amd64-[\d.]+\.msi$`},
	{ID: "tailscale", Name: "Tailscale", Vendor: "Tailscale", Group: "Remote access and networking", Kind: "msi", Signer: `CN="?Tailscale Inc`,
		URL: "https://pkgs.tailscale.com/stable/tailscale-setup-latest-amd64.msi", Note: "Sign in once after the install: it joins your tailnet then."},
	{ID: "wireshark", Name: "Wireshark", Vendor: "Wireshark Foundation", Group: "Remote access and networking", Kind: "exe", Signer: `CN="?Wireshark Foundation`,
		Args: []string{"/S", "/desktopicon=no", "/quicklaunchicon=no"}, URL: "https://www.wireshark.org/download/win64/Wireshark-latest-x64.exe",
		Note: "Capturing needs Npcap, which has no silent installer for free use: install it once from npcap.com."},

	// Utilities and media
	{ID: "7zip", Name: "7-Zip", Vendor: "Igor Pavlov", Group: "Utilities and media", Kind: "msi",
		Repo: "ip7z/7zip", Match: `^7z\d+-x64\.msi$`},
	{ID: "keepassxc", Name: "KeePassXC", Vendor: "KeePassXC", Group: "Utilities and media", Kind: "msi",
		Repo: "keepassxreboot/keepassxc", Match: `^KeePassXC-[\d.]+-Win64\.msi$`},
	{ID: "sharex", Name: "ShareX", Vendor: "ShareX", Group: "Utilities and media", Kind: "exe",
		Args: []string{"/VERYSILENT", "/NORESTART"}, Repo: "ShareX/ShareX", Match: `^ShareX-[\d.]+-setup-x64\.exe$`},
	{ID: "greenshot", Name: "Greenshot", Vendor: "Greenshot", Group: "Utilities and media", Kind: "exe",
		Args: []string{"/VERYSILENT", "/NORESTART", "/SUPPRESSMSGBOXES"}, Repo: "greenshot/greenshot", Match: `^Greenshot-INSTALLER-[\d.]+-RELEASE\.exe$`},
	{ID: "vlc", Name: "VLC media player", Vendor: "VideoLAN", Group: "Utilities and media", Kind: "msi", Signer: `CN="?VideoLAN`,
		Dir: "https://get.videolan.org/vlc/last/win64/", Match: `^vlc-[\d.]+-win64\.msi$`},
	{ID: "obs", Name: "OBS Studio", Vendor: "OBS Project", Group: "Utilities and media", Kind: "exe",
		Args: []string{"/S"}, Repo: "obsproject/obs-studio", Match: `^OBS-Studio-[\d.]+-Windows-x64-Installer\.exe$`},
	{ID: "vcredist", Name: "Visual C++ Redistributable (x64)", Vendor: "Microsoft", Group: "Utilities and media", Kind: "exe", Signer: `CN="?Microsoft Corporation`,
		Args: []string{"/install", "/quiet", "/norestart"}, URL: "https://aka.ms/vs/17/release/vc_redist.x64.exe"},
}

func winAppByID(id string) *winApp {
	i := slices.IndexFunc(winApps, func(x winApp) bool { return x.ID == id })
	if i < 0 {
		return nil
	}
	return &winApps[i]
}

// winAppJob is one resolved download, as the guest script reads it.
type winAppJob struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Kind   string   `json:"kind"`
	Args   []string `json:"args"`
	Signer string   `json:"signer,omitempty"`
	SHA256 string   `json:"sha256,omitempty"`
}

var appHTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" || len(via) > 5 {
		return errors.New("refusing a redirect away from https")
	}
	return nil
}}

func appGet(ctx context.Context, u string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "servermgr")
	res, err := appHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, max))
}

var (
	appHref = regexp.MustCompile(`href="([^"?#]+)"`) // a name, or a path ending in one
	appSHA  = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)
)

// resolve finds the app's current download.
func (x *winApp) resolve(ctx context.Context) (*winAppJob, error) {
	j := &winAppJob{ID: x.ID, Name: x.Name, Kind: x.Kind, Args: x.Args, Signer: x.Signer}
	if j.Args == nil {
		j.Args = []string{}
	}
	re := regexp.MustCompile(x.Match)
	switch {
	case x.URL != "":
		j.URL = x.URL
	case x.Repo != "":
		b, err := appGet(ctx, "https://api.github.com/repos/"+x.Repo+"/releases/latest", 4<<20)
		if err != nil {
			return nil, err
		}
		var rel struct {
			Assets []struct {
				Name   string `json:"name"`
				URL    string `json:"browser_download_url"`
				Digest string `json:"digest"`
			} `json:"assets"`
		}
		if err := json.Unmarshal(b, &rel); err != nil {
			return nil, err
		}
		for _, as := range rel.Assets {
			if re.MatchString(as.Name) && strings.HasPrefix(as.URL, "https://github.com/"+x.Repo+"/") {
				j.URL = as.URL
				if m := appSHA.FindStringSubmatch(as.Digest); m != nil {
					j.SHA256 = m[1]
				}
				break
			}
		}
		if j.URL == "" {
			return nil, fmt.Errorf("no file matching %s in %s's latest release", x.Match, x.Repo)
		}
	case x.Dir != "":
		b, err := appGet(ctx, x.Dir, 2<<20)
		if err != nil {
			return nil, err
		}
		best := ""
		for _, m := range appHref.FindAllSubmatch(b, -1) {
			if n, err := url.PathUnescape(path.Base(string(m[1]))); err == nil && re.MatchString(n) && (best == "" || naturalAfter(n, best)) {
				best = n
			}
		}
		if best == "" {
			return nil, fmt.Errorf("no file matching %s at %s", x.Match, x.Dir)
		}
		j.URL = x.Dir + url.PathEscape(path.Base(best))
	}
	if j.Signer == "" && j.SHA256 == "" {
		return nil, fmt.Errorf("%s: no way to verify the download", x.Name)
	}
	return j, nil
}

// naturalAfter reports whether a sorts after b with digit runs compared as
// numbers (vlc-3.0.10 after vlc-3.0.9).
func naturalAfter(a, b string) bool {
	num := regexp.MustCompile(`\d+|\D+`)
	pa, pb := num.FindAllString(a, -1), num.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if pa[i] == pb[i] {
			continue
		}
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea == nil && eb == nil {
			return na > nb
		}
		return pa[i] > pb[i]
	}
	return len(pa) > len(pb)
}

// resolveWinApps checks ids against the catalog and resolves them.
func resolveWinApps(ctx context.Context, ids []string) ([]winAppJob, error) {
	if len(ids) > len(winApps) {
		return nil, errors.New("too many applications")
	}
	jobs := []winAppJob{}
	for i, id := range ids {
		x := winAppByID(id)
		if x == nil || slices.Contains(ids[:i], id) {
			return nil, fmt.Errorf("unknown application %q", id)
		}
		j, err := x.resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", x.Name, err)
		}
		jobs = append(jobs, *j)
	}
	return jobs, nil
}

func appsScript() []byte {
	b, err := windowsFiles.ReadFile("windows/apps.ps1")
	if err != nil {
		panic(err)
	}
	// Windows PowerShell 5.1 reads a script without a BOM as ANSI.
	return crlf(append([]byte("\ufeff"), bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))...))
}

// GET /api/v1/windows/apps: the catalog.
func (a *API) handleWindowsApps(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, winApps)
}

// The script, its list and log live in a folder under C:\Windows, which
// only SYSTEM and Administrators can write: a local user cannot plant a
// file there for SYSTEM to run.
const (
	winAppsDir = `C:\Windows\servermgr`
	winAppsLog = winAppsDir + `\apps.log`
)

// POST .../windows/apps {apps}: installs applications in the running VM in
// the background; the answer's pid is followed with .../windows/apps/status.
func (a *API) handlePVEWinAppsInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Apps []string `json:"apps"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Apps) == 0 {
		writeErr(w, http.StatusBadRequest, "pick at least one application")
		return
	}
	for i, id := range req.Apps {
		if winAppByID(id) == nil || slices.Contains(req.Apps[:i], id) {
			writeErr(w, http.StatusBadRequest, "unknown application "+strconv.Quote(id))
			return
		}
	}
	s, _, gp, vmid, ok := a.winGuest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	jobs, err := resolveWinApps(ctx, req.Apps)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	jb, _ := json.Marshal(jobs)
	fail := func(err error) {
		a.audit(r, "", "proxmox.windows_apps", target, map[string]any{"apps": req.Apps, "error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
	}
	if _, err := agentRun(ctx, s.Cl, gp, psCommand(`New-Item -ItemType Directory -Force -Path '`+winAppsDir+`' | Out-Null; 'ok'`)); err != nil {
		fail(err)
		return
	}
	for name, data := range map[string][]byte{`\apps.ps1`: appsScript(), `\apps.json`: jb} {
		if err := s.Cl.Do(ctx, http.MethodPost, gp+"/agent/file-write", url.Values{"file": {winAppsDir + name}, "content": {string(data)}}, nil); err != nil {
			fail(agentErr(err))
			return
		}
	}
	pid, err := agentExec(ctx, s.Cl, gp, []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", winAppsDir + `\apps.ps1`, "-Json", winAppsDir + `\apps.json`, "-LogFile", winAppsLog})
	if err != nil {
		fail(err)
		return
	}
	a.audit(r, "", "proxmox.windows_apps", target, map[string]any{"apps": req.Apps}, true)
	writeJSON(w, http.StatusAccepted, map[string]any{"pid": pid})
}

// GET .../windows/apps/status?pid=: whether the install finished, and its
// log so far.
func (a *API) handlePVEWinAppsStatus(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.Atoi(r.URL.Query().Get("pid"))
	if err != nil || pid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad pid")
		return
	}
	s, _, gp, _, ok := a.winGuest(w, r)
	if !ok {
		return
	}
	var st agentExecStatus
	if err := s.Cl.Do(r.Context(), http.MethodGet, gp+"/agent/exec-status", url.Values{"pid": {strconv.Itoa(pid)}}, &st); err != nil {
		writeErr(w, http.StatusBadGateway, agentErr(err).Error())
		return
	}
	var f struct {
		Content string `json:"content"`
	}
	_ = s.Cl.Do(r.Context(), http.MethodGet, gp+"/agent/file-read", url.Values{"file": {winAppsLog}}, &f)
	lines := []string{}
	for _, l := range strings.Split(f.Content, "\n") {
		if l = cleanText(strings.TrimSpace(strings.TrimPrefix(l, "\ufeff")), 300); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	writeJSON(w, http.StatusOK, map[string]any{"done": st.Exited == 1, "exit_code": st.ExitCode, "log": lines})
}
