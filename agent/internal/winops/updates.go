//go:build windows

package winops

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Windows Update through its COM API (the same one the Settings app uses),
// as SYSTEM. Results have the shape of the Linux package updates: name is
// the KB number (or the update ID), current the MSRC severity, candidate
// the title and repository the category.

type pkgUpdate struct {
	Name       string `json:"name"`
	Current    string `json:"current"`
	Candidate  string `json:"candidate"`
	Repository string `json:"repository"`
	Security   bool   `json:"security"`
}

const wuSearch = `$session = New-Object -ComObject Microsoft.Update.Session
$session.ClientApplicationID = 'servermgr'
$found = $session.CreateUpdateSearcher().Search("IsInstalled=0 and IsHidden=0 and Type='Software'")
`

const wuCheck = wuSearch + `$list = @(foreach ($u in $found.Updates) {
    $kb = @($u.KBArticleIDs) | Select-Object -First 1
    [pscustomobject]@{
        id         = [string]$u.Identity.UpdateID
        kb         = $(if ($kb) { 'KB' + $kb } else { '' })
        title      = [string]$u.Title
        severity   = [string]$u.MsrcSeverity
        categories = @($u.Categories | ForEach-Object { [string]$_.Name })
    }
})
$reboot = (New-Object -ComObject Microsoft.Update.SystemInfo).RebootRequired
[pscustomobject]@{ updates = $list; reboot_required = [bool]$reboot } | ConvertTo-Json -Depth 4 -Compress
`

func checkUpdates(ctx context.Context, logf logFunc) (any, error) {
	logf("searching Windows Update")
	var res struct {
		Updates []struct {
			ID, KB, Title, Severity string
			Categories              []string
		} `json:"updates"`
		Reboot bool `json:"reboot_required"`
	}
	if err := psJSON(ctx, wuCheck, &res); err != nil {
		return nil, err
	}
	updates := []pkgUpdate{}
	security := 0
	for _, u := range res.Updates {
		p := pkgUpdate{Name: u.KB, Current: u.Severity, Candidate: u.Title, Repository: strings.Join(u.Categories, ", ")}
		if p.Name == "" {
			p.Name = u.ID
		}
		p.Security = u.Severity != ""
		for _, c := range u.Categories {
			if c == "Security Updates" || c == "Critical Updates" {
				p.Security = true
			}
		}
		if p.Security {
			security++
		}
		updates = append(updates, p)
	}
	logf("%d update(s) available (%d security)", len(updates), security)
	return map[string]any{
		"updates":         updates,
		"count":           len(updates),
		"security_count":  security,
		"reboot_required": res.Reboot,
	}, nil
}

// An update is picked by its KB number or its update ID.
var updateKey = regexp.MustCompile(`^(KB\d{4,8}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// The script prints progress lines and, last, one result line.
const wuResultMark = "__SERVERMGR_RESULT__"

const wuInstall = wuSearch + `$codes = @{ 0 = 'not started'; 1 = 'in progress'; 2 = 'installed'; 3 = 'installed with errors'; 4 = 'failed'; 5 = 'aborted' }
$coll = New-Object -ComObject Microsoft.Update.UpdateColl
foreach ($u in $found.Updates) {
    $kb = @($u.KBArticleIDs) | Select-Object -First 1
    $keys = @([string]$u.Identity.UpdateID)
    if ($kb) { $keys += 'KB' + $kb }
    if ($want.Count -gt 0 -and -not ($keys | Where-Object { $want -contains $_ })) { continue }
    if (-not $u.EulaAccepted) { $u.AcceptEula() }
    [void]$coll.Add($u)
    "selected: $($u.Title)"
}
$failed = @()
$reboot = $false
if ($coll.Count -eq 0) {
    'nothing to install'
} else {
    "downloading $($coll.Count) update(s)"
    $dl = $session.CreateUpdateDownloader()
    $dl.Updates = $coll
    $d = $dl.Download()
    "download: $($codes[[int]$d.ResultCode])"
    $inst = $session.CreateUpdateInstaller()
    $inst.Updates = $coll
    'installing'
    $r = $inst.Install()
    for ($i = 0; $i -lt $coll.Count; $i++) {
        $c = [int]$r.GetUpdateResult($i).ResultCode
        "$($coll.Item($i).Title): $($codes[$c])"
        if ($c -ge 4) { $failed += [string]$coll.Item($i).Title }
    }
    $reboot = [bool]$r.RebootRequired
}
$reboot = $reboot -or [bool](New-Object -ComObject Microsoft.Update.SystemInfo).RebootRequired
'` + wuResultMark + `' + ([pscustomobject]@{ failed = $failed; reboot_required = $reboot } | ConvertTo-Json -Compress)
`

func upgradePackages(ctx context.Context, p map[string]string, logf logFunc) (any, error) {
	want := strings.Fields(p["packages"])
	quoted := make([]string, len(want))
	for i, w := range want {
		if !updateKey.MatchString(w) {
			return nil, fmt.Errorf("invalid update %q (a KB number or an update ID)", w)
		}
		quoted[i] = psString(w)
	}
	script := "$want = @(" + strings.Join(quoted, ", ") + ")\n" + wuInstall
	var res struct {
		Failed []string `json:"failed"`
		Reboot bool     `json:"reboot_required"`
	}
	got := false
	err := psStream(ctx, script, func(line string) {
		if r, ok := strings.CutPrefix(line, wuResultMark); ok {
			got = json.Unmarshal([]byte(r), &res) == nil
			return
		}
		logf("%s", line)
	})
	if err != nil {
		return nil, err
	}
	if !got {
		return nil, errors.New("Windows Update ended without a result")
	}
	if len(res.Failed) > 0 {
		return nil, fmt.Errorf("%d update(s) failed: %s", len(res.Failed), strings.Join(res.Failed, "; "))
	}
	if res.Reboot {
		logf("a reboot is needed to finish")
	}
	logf("updates finished")
	return map[string]any{"reboot_required": res.Reboot}, nil
}

// psStream runs script like powershell does, handing each output line to
// line as it comes.
func psStream(ctx context.Context, script string, line func(string)) error {
	script = "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n" +
		"[Console]::OutputEncoding = [Text.Encoding]::UTF8\n" + script
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff")); l != "" {
			line(l)
		}
	}
	_, _ = io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if len(msg) > 600 {
			msg = "…" + msg[len(msg)-600:]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("powershell: %s", msg)
	}
	return nil
}
