package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"
	"unicode"

	"github.com/jacksonm36/servermgr/panel/internal/isofs"
	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
	"github.com/jacksonm36/servermgr/panel/internal/store"
)

// Windows templates with cloud-init (Cloudbase-Init).
//
// A build installs Windows unattended from the admin's ISO: servermgr
// writes an answer CD (autounattend.xml plus windows/prepare.ps1), uploads
// it next to the ISO, creates a UEFI + TPM VM with the installer, the
// virtio-win CD and the answer CD, and presses Enter at "Press any key to
// boot from CD". Windows installs itself, logs on once and runs the
// prepare script, which installs the drivers and guest agent, tunes
// Windows for Proxmox, applies the Standard or Hardened profile, installs
// Cloudbase-Init and runs Sysprep. servermgr follows its log through the
// guest agent; when the VM shuts down after Sysprep it swaps the CDs for a
// cloud-init drive and makes the VM a template.
//
// An existing Windows VM can get the same script on a CD (windows-prep)
// and be turned into a template afterwards (windows-template).
//
// Deploying from a Windows template sets the Administrator password,
// hostname and network through cloud-init; see provisionVM.

//go:embed windows/prepare.ps1 windows/autounattend.xml windows/apps.ps1
var windowsFiles embed.FS

var winUnattend = template.Must(template.New("autounattend.xml").Funcs(template.FuncMap{"inc": func(i int) int { return i + 1 }}).
	ParseFS(windowsFiles, "windows/autounattend.xml"))

type winEdition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OSType string `json:"ostype"`
	Virtio string `json:"virtio"` // driver folder on the virtio-win CD
	Index  int    `json:"index"`  // image in install.wim of the evaluation ISO
	Client bool   `json:"client"`
}

var winEditions = []winEdition{
	{"win11", "Windows 11", "win11", "w11", 1, true},
	{"win10", "Windows 10", "win10", "w10", 1, true},
	{"ws2025", "Windows Server 2025", "win11", "2k25", 2, false},
	{"ws2022", "Windows Server 2022", "win11", "2k22", 2, false},
	{"ws2019", "Windows Server 2019", "win10", "2k19", 2, false},
}

func winEditionByID(id string) *winEdition {
	i := slices.IndexFunc(winEditions, func(e winEdition) bool { return e.ID == id })
	if i < 0 {
		return nil
	}
	return &winEditions[i]
}

var winProfiles = []string{"standard", "hardened"}

var (
	pveISOVol   = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_.-]{0,62}):iso/[A-Za-z0-9][A-Za-z0-9_.+-]{0,200}\.(iso|img)$`)
	winLocale   = regexp.MustCompile(`^[a-z]{2,3}-[A-Z]{2}$`)
	winKeyboard = regexp.MustCompile(`^([a-z]{2,3}-[A-Z]{2}|[0-9a-fA-F]{4}:[0-9a-fA-F]{8})$`)
	winTimeZone = regexp.MustCompile(`^[A-Za-z][A-Za-z .()+-]{1,62}$`)
	winKey      = regexp.MustCompile(`^([A-Z0-9]{5}-){4}[A-Z0-9]{5}$`)
	winHost     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,13}[a-z0-9])?$`)
)

// winPasswordProblem says what is wrong with a Windows password, or "".
// Windows wants three of four character kinds; the Hardened profile wants
// at least 12 characters, which servermgr asks of every Windows VM.
func winPasswordProblem(pw string) string {
	if len(pw) < 12 || len(pw) > 120 {
		return "Windows password: 12 to 120 characters"
	}
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsControl(r):
			return "Windows password: no control characters"
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	if n < 3 {
		return "Windows password: use three of lower case, upper case, digits and symbols"
	}
	if strings.Contains(strings.ToLower(pw), "administrator") {
		return "Windows password: must not contain the user name"
	}
	return ""
}

// winBuildPassword is the throwaway Administrator password of a build.
// It only lives in the answer CD, which is deleted after the build, and
// Cloudbase-Init replaces it on every deployed VM.
func winBuildPassword() string {
	const sets = "abcdefghijkmnpqrstuvwxyz|ABCDEFGHJKLMNPQRSTUVWXYZ|23456789|-_.!+"
	groups := strings.Split(sets, "|")
	all := strings.Join(groups, "")
	pick := func(s string) byte {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(s))))
		if err != nil {
			panic(err)
		}
		return s[n.Int64()]
	}
	b := make([]byte, 0, 24)
	for _, g := range groups {
		b = append(b, pick(g))
	}
	for len(b) < 24 {
		b = append(b, pick(all))
	}
	return string(b)
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func prepareScript() []byte {
	b, err := windowsFiles.ReadFile("windows/prepare.ps1")
	if err != nil {
		panic(err)
	}
	// Windows PowerShell 5.1 reads a script without a BOM as ANSI.
	return append([]byte("\ufeff"), bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))...)
}

func crlf(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\n"), []byte("\r\n")) }

// winGenericKeys are Microsoft's generic setup keys for the images of the
// Windows 10/11 multi-edition ISO, by index. They pick the edition and
// activate nothing. Setup refuses a key of another edition than the image
// ("Windows cannot read the <ProductKey> setting"), so Setup always gets
// the image's generic key and the admin's own key is installed afterwards.
var winGenericKeys = map[int]string{
	1:  "YTMG3-N6DKC-DKB77-7M9GH-8HVX7", // Home
	2:  "4CPRK-NM3K3-X6XXQ-RXX86-WXCHW", // Home N
	3:  "BT79Q-G7N6G-PGBYW-4YWX6-6F4BT", // Home Single Language
	4:  "YNMGQ-8RYV3-4PGQ3-C8XTP-7CFBY", // Education
	5:  "84NGF-MHBT6-FXBX8-QWJK7-DRR8H", // Education N
	6:  "VK7JG-NPHTM-C97JM-9MPGT-3V66T", // Pro
	7:  "2B87N-8KFHP-DKV6R-Y2C8J-PKCKT", // Pro N
	8:  "8PTT6-RNW4C-6V7J2-C2D3X-MHBPB", // Pro Education
	9:  "GJTYN-HDMQY-FRR76-HVGC7-QPF8P", // Pro Education N
	10: "DXG7C-N36C4-C4HTG-X4T3X-2YV77", // Pro for Workstations
	11: "WYPNQ-8C467-V2W6J-TX4WX-WT2RQ", // Pro for Workstations N
}

// winEvalISO: Microsoft's evaluation ISOs say so in their file name
// (…CLIENTENTERPRISEEVAL…, …SERVER_EVAL…); they take no key at all.
func winEvalISO(volid string) bool { return strings.Contains(strings.ToUpper(volid), "EVAL") }

// winSetupKey splits the build's key into the one Setup gets and the
// admin's own one, installed after Setup.
func winSetupKey(ed *winEdition, req pveWinBuildReq) (setup, own string) {
	if winEvalISO(req.ISO) {
		return "", ""
	}
	g, ok := winGenericKeys[req.ImageIndex]
	if !ed.Client || !ok {
		return req.ProductKey, ""
	}
	if req.ProductKey == g || slices.Contains(slices.Collect(maps.Values(winGenericKeys)), req.ProductKey) {
		return g, ""
	}
	return g, req.ProductKey
}

type winUnattendData struct {
	Locale, Keyboard, TimeZone, ProductKey, Password, Profile string
	Index                                                     int
	DriverPaths                                               []string
}

// answerISO is the CD a build boots with: the answer file and the script.
func answerISO(ed *winEdition, req pveWinBuildReq, profile, password string) ([]byte, error) {
	setupKey, ownKey := winSetupKey(ed, req)
	d := winUnattendData{
		Locale: xmlText(req.Locale), Keyboard: xmlText(req.Keyboard), TimeZone: xmlText(req.TimeZone), ProductKey: xmlText(setupKey),
		Password: xmlText(password), Profile: map[string]string{"standard": "Standard", "hardened": "Hardened"}[profile], Index: req.ImageIndex,
	}
	// The virtio-win CD's letter depends on how many drives Setup sees.
	for _, letter := range "DEFGHIJ" {
		for _, drv := range []string{"vioscsi", "NetKVM", "Balloon", "vioserial"} {
			d.DriverPaths = append(d.DriverPaths, fmt.Sprintf(`%c:\%s\%s\amd64`, letter, drv, ed.Virtio))
		}
	}
	var x bytes.Buffer
	if err := winUnattend.Execute(&x, d); err != nil {
		return nil, err
	}
	files := []isofs.File{
		{Name: "autounattend.xml", Short: "AUTOUNAT.XML", Data: crlf(x.Bytes())},
		{Name: "servermgr-prepare.ps1", Short: "SMPREP.PS1", Data: crlf(prepareScript())},
	}
	// The admin's own key goes in after Setup, where an edition mismatch
	// cannot stop the install.
	if ownKey != "" {
		files = append(files, isofs.File{Name: "servermgr-key.txt", Short: "SMKEY.TXT", Data: []byte(ownKey)})
	}
	// Applications to install: the prepare script runs servermgr-apps.ps1
	// with this list when both are next to it.
	if len(req.appJobs) > 0 {
		jb, err := json.Marshal(req.appJobs)
		if err != nil {
			return nil, err
		}
		files = append(files,
			isofs.File{Name: "servermgr-apps.ps1", Short: "SMAPPS.PS1", Data: appsScript()},
			isofs.File{Name: "servermgr-apps.json", Short: "SMAPPS.JSO", Data: jb})
	}
	return isofs.Build("SMWINBUILD", files, time.Now())
}

func prepISO() ([]byte, error) {
	return isofs.Build("SMPREP", []isofs.File{{Name: "servermgr-prepare.ps1", Short: "SMPREP.PS1", Data: crlf(prepareScript())}}, time.Now())
}

// GET /api/v1/windows/prepare.ps1: the script, to read or copy by hand.
func (a *API) handleWindowsScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="servermgr-prepare.ps1"`)
	_, _ = w.Write(crlf(prepareScript()))
}

func (a *API) handleWindowsEditions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"editions": winEditions, "profiles": winProfiles})
}

// POST /api/v1/proxmox/{id}/nodes/{node}/windows-iso has the node download
// the retail Windows 10/11 ISO in a language from Microsoft into an ISO
// storage (the evaluation ISOs come from the ISO library). The link is
// looked up at Microsoft right now and never comes from the browser. It
// answers the ISO's volume, the image index of Pro and, unless the ISO is
// already there, the download task.
func (a *API) handlePVEWindowsISO(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Edition string `json:"edition"`
		Locale  string `json:"locale"`
		Storage string `json:"storage"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	switch {
	case !proxmox.HasRetailISO(req.Edition):
		bad("Microsoft offers a retail ISO of Windows 10 and 11 only")
		return
	case !winLocale.MatchString(req.Locale):
		bad("language: like en-US")
		return
	case !pveStorage.MatchString(req.Storage):
		bad("bad storage")
		return
	}
	s, node, ok := a.pveAdminNode(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	iso, err := proxmox.RetailWindowsISO(ctx, req.Edition, req.Locale)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	volid := req.Storage + ":iso/" + iso.File
	out := map[string]any{"volid": volid, "file": iso.File, "index": proxmox.WinRetailIndex}
	have, err := isoOnStorage(ctx, s.Cl, node, volid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if have {
		writeJSON(w, http.StatusOK, out)
		return
	}
	form := url.Values{"url": {iso.URL}, "content": {"iso"}, "filename": {iso.File}}
	target := s.C.Name + "/" + node + "/" + volid
	var upid string
	if err := s.Cl.Do(ctx, http.MethodPost, storagePath(node, req.Storage)+"/download-url", form, &upid); err != nil {
		a.audit(r, "", "proxmox.download_url", target, map[string]string{"source": "microsoft retail", "error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.download_url", target, map[string]string{"source": "microsoft retail"}, true)
	out["upid"] = upid
	writeJSON(w, http.StatusAccepted, out)
}

type pveWinBuildReq struct {
	Edition      string   `json:"edition"`
	Node         string   `json:"node"`
	ISO          string   `json:"iso"`         // storage:iso/file.iso
	DriversISO   string   `json:"drivers_iso"` // virtio-win
	DiskStorage  string   `json:"disk_storage"`
	Bridge       string   `json:"bridge"`
	VLAN         int      `json:"vlan"` // 0 = untagged
	Cores        int      `json:"cores"`
	MemoryMB     int      `json:"memory_mb"`
	DiskGB       int      `json:"disk_gb"`
	ImageIndex   int      `json:"image_index"`
	ProductKey   string   `json:"product_key"`
	Locale       string   `json:"locale"`
	Keyboard     string   `json:"keyboard"`
	TimeZone     string   `json:"timezone"`
	Profiles     []string `json:"profiles"`
	Apps         []string `json:"apps"` // ids from winApps, installed during the build
	appJobs      []winAppJob
	NamePrefix   string `json:"name_prefix"`
	answerTarget string // storage for the answer CD: the installer ISO's
}

func (r *pveWinBuildReq) check(ed *winEdition) string {
	if r.Cores == 0 {
		r.Cores = 2
	}
	if r.MemoryMB == 0 {
		r.MemoryMB = 4096
	}
	if r.DiskGB == 0 {
		r.DiskGB = 64
	}
	if r.ImageIndex == 0 {
		r.ImageIndex = ed.Index
	}
	r.Locale = strings.TrimSpace(r.Locale)
	if r.Locale == "" {
		r.Locale = "en-US"
	}
	if r.Keyboard = strings.TrimSpace(r.Keyboard); r.Keyboard == "" {
		r.Keyboard = r.Locale
	}
	if r.TimeZone = strings.TrimSpace(r.TimeZone); r.TimeZone == "" {
		r.TimeZone = "UTC"
	}
	r.ProductKey = strings.ToUpper(strings.TrimSpace(r.ProductKey))
	if r.NamePrefix == "" {
		r.NamePrefix = ed.ID
	}
	m := pveISOVol.FindStringSubmatch(r.ISO)
	switch {
	case !pveNode.MatchString(r.Node):
		return "pick a node"
	case m == nil:
		return "pick the Windows installer ISO"
	case !pveISOVol.MatchString(r.DriversISO):
		return "pick the virtio-win drivers ISO"
	case r.ISO == r.DriversISO:
		return "the drivers ISO is not the Windows installer"
	case !pveStorage.MatchString(r.DiskStorage) || !pveBridge.MatchString(r.Bridge):
		return "pick a disk storage and a network bridge"
	case r.VLAN < 0 || r.VLAN > 4094:
		return "VLAN: 1-4094, or 0 for untagged"
	case r.Cores < 1 || r.Cores > 64 || r.MemoryMB < 2048 || r.MemoryMB > 1<<20 || r.DiskGB < 32 || r.DiskGB > 10000:
		return "resources: 1-64 cores, at least 2048 MB memory and a 32 GB disk"
	case r.ImageIndex < 1 || r.ImageIndex > 32:
		return "image index: 1 to 32"
	case r.ProductKey != "" && !winKey.MatchString(r.ProductKey):
		return "product key: five groups of five letters or digits"
	case r.ProductKey != "" && winEvalISO(r.ISO):
		return "evaluation ISOs take no product key: leave it empty, or pick Microsoft's multi-edition ISO"
	case !winLocale.MatchString(r.Locale) || !winKeyboard.MatchString(r.Keyboard) || !winTimeZone.MatchString(r.TimeZone):
		return "language, keyboard or time zone not valid (like en-US, hu-HU, Central Europe Standard Time)"
	case len(r.Profiles) == 0:
		return "pick Standard, Hardened or both"
	}
	for i, id := range r.Apps {
		if winAppByID(id) == nil || slices.Contains(r.Apps[:i], id) {
			return "unknown application " + strconv.Quote(id)
		}
	}
	for i, p := range r.Profiles {
		if !slices.Contains(winProfiles, p) || slices.Contains(r.Profiles[:i], p) {
			return "profiles: standard and/or hardened"
		}
		if !pveHost.MatchString(r.NamePrefix + "-" + p) {
			return "name: lowercase letters, digits and hyphens"
		}
	}
	r.answerTarget = m[1]
	return ""
}

// isoOnStorage reports whether volid is an ISO on its storage on node.
func isoOnStorage(ctx context.Context, cl *proxmox.Client, node, volid string) (bool, error) {
	st, _, _ := strings.Cut(volid, ":")
	var list []struct {
		VolID string `json:"volid"`
	}
	if err := cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(st)+"/content", url.Values{"content": {"iso"}}, &list); err != nil {
		return false, err
	}
	return slices.ContainsFunc(list, func(x struct {
		VolID string `json:"volid"`
	}) bool {
		return x.VolID == volid
	}), nil
}

// POST /api/v1/proxmox/{id}/windows-templates builds one template per
// profile, side by side.
func (a *API) handlePVEWindowsBuild(w http.ResponseWriter, r *http.Request) {
	var req pveWinBuildReq
	if !readJSON(w, r, &req) {
		return
	}
	bad := func(m string) { writeErr(w, http.StatusBadRequest, m) }
	ed := winEditionByID(req.Edition)
	if ed == nil {
		bad("pick a Windows edition")
		return
	}
	if msg := req.check(ed); msg != "" {
		bad(msg)
		return
	}
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusForbidden, "templates are made by the platform's administrators")
		return
	}
	t, err := templateTargets(r.Context(), s.Cl, req.Node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	switch {
	case !slices.Contains(t.Nodes, req.Node):
		bad("pick an online node")
		return
	case !slices.Contains(t.DiskStorages, req.DiskStorage):
		bad("pick a storage for the template's disks")
		return
	case !slices.Contains(t.Bridges, req.Bridge):
		bad("pick a network bridge")
		return
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	for _, prof := range req.Profiles {
		name := req.NamePrefix + "-" + prof
		if i := slices.IndexFunc(res, func(x proxmox.Resource) bool { return x.Type == "qemu" && x.Name == name }); i >= 0 {
			writeErr(w, http.StatusConflict, fmt.Sprintf("%s already exists (VM %d); delete it or pick another name prefix", name, res[i].VMID))
			return
		}
	}
	for _, v := range []string{req.ISO, req.DriversISO} {
		ok, err := isoOnStorage(r.Context(), s.Cl, req.Node, v)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if !ok {
			bad(v + " is not on " + req.Node)
			return
		}
	}
	if len(req.Apps) > 0 {
		jobs, err := resolveWinApps(r.Context(), req.Apps)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		req.appJobs = jobs
	}
	who := whoFrom(r)
	var runs []*store.ProxmoxProvision
	for _, prof := range req.Profiles {
		p := &store.ProxmoxProvision{ID: store.NewUUID(), ClusterID: s.C.ID, Name: "template " + req.NamePrefix + "-" + prof, Status: "running", CreatedBy: who.Actor}
		if err := a.Store.CreateProvision(r.Context(), p); err != nil {
			a.internal(w, err)
			return
		}
		runs = append(runs, p)
	}
	a.audit(r, "", "proxmox.windows_template", s.C.Name+"/"+req.NamePrefix, req, true)
	for i, prof := range req.Profiles {
		go a.buildWindowsTemplate(s, runs[i], ed, req, prof)
	}
	writeJSON(w, http.StatusAccepted, runs)
}

// winPoll is how often a build looks at the VM; a var for tests.
var winPoll = 15 * time.Second

const winBuildLog = `C:\Windows\Temp\servermgr-build.log`

func (a *API) buildWindowsTemplate(s *pveScope, p *store.ProxmoxProvision, ed *winEdition, req pveWinBuildReq, profile string) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 6*time.Hour)
	defer cancel()
	logf := a.provisionLog(p)
	created := func(vmid int) {
		p.VMID = &vmid
		_ = a.Store.UpdateProvision(a.baseCtx(), p, false)
	}
	vmid, err := a.winBuild(ctx, s.Cl, ed, req, profile, created, logf)
	if vmid != 0 {
		p.VMID = &vmid
	}
	if err == nil {
		logf("done: template %d (%s, %s) is ready; deploy it from Deploy or New VM", vmid, ed.Name, profile)
	}
	a.finishProvision(p, err, logf)
}

func (a *API) winBuild(ctx context.Context, cl *proxmox.Client, ed *winEdition, req pveWinBuildReq, profile string, created func(int), logf func(string, ...any)) (int, error) {
	node := req.Node
	nodePath := "/nodes/" + url.PathEscape(node)
	name := req.NamePrefix + "-" + profile

	iso, err := answerISO(ed, req, profile, winBuildPassword())
	if err != nil {
		return 0, err
	}
	file := fmt.Sprintf("servermgr-answer-%s-%d.iso", name, time.Now().Unix())
	answer := req.answerTarget + ":iso/" + file
	logf("uploading the answer CD (%d KB) to %s", len(iso)>>10, req.answerTarget)
	upid, err := cl.Upload(ctx, nodePath+"/storage/"+url.PathEscape(req.answerTarget)+"/upload", map[string]string{"content": "iso"}, file, bytes.NewReader(iso), int64(len(iso)))
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	if err := cl.WaitTask(ctx, node, upid); err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	// The answer CD holds the build's password: it goes whatever happens.
	defer func() {
		dctx, cancel := context.WithTimeout(a.baseCtx(), time.Minute)
		defer cancel()
		var upid string
		if err := cl.Do(dctx, http.MethodDelete, nodePath+"/storage/"+url.PathEscape(req.answerTarget)+"/content/"+url.PathEscape(answer), nil, &upid); err != nil {
			logf("warning: could not delete the answer CD %s: %v (delete it by hand)", answer, err)
		} else {
			logf("deleted the answer CD")
		}
	}()

	a.pveCreateMu.Lock()
	vmid, err := func() (int, error) {
		defer a.pveCreateMu.Unlock()
		var next flexString
		if err := cl.Do(ctx, http.MethodGet, "/cluster/nextid", nil, &next); err != nil {
			return 0, err
		}
		vmid, err := strconv.Atoi(string(next))
		if err != nil {
			return 0, fmt.Errorf("unexpected nextid %q", next)
		}
		d := req.DiskStorage
		cfg := url.Values{
			"vmid":      {strconv.Itoa(vmid)},
			"name":      {name},
			"ostype":    {ed.OSType},
			"bios":      {"ovmf"},
			"machine":   {"q35"},
			"cpu":       {"x86-64-v2-AES"},
			"cores":     {strconv.Itoa(req.Cores)},
			"memory":    {strconv.Itoa(req.MemoryMB)},
			"scsihw":    {"virtio-scsi-single"},
			"scsi0":     {fmt.Sprintf("%s:%d,discard=on,iothread=1,ssd=1", d, req.DiskGB)},
			"efidisk0":  {d + ":1,efitype=4m,pre-enrolled-keys=1"},
			"tpmstate0": {d + ":1,version=v2.0"},
			"ide2":      {req.ISO + ",media=cdrom"},
			"ide0":      {req.DriversISO + ",media=cdrom"},
			"ide3":      {answer + ",media=cdrom"},
			"boot":      {"order=scsi0;ide2"},
			"net0":      {"virtio,bridge=" + req.Bridge + vlanOpt(req.VLAN) + ",firewall=1"},
			"agent":     {"enabled=1"},
			"vga":       {"std"},
			"tags":      {"servermgr;windows;win-" + profile + ";" + ed.ID},
			"description": {fmt.Sprintf("%s (%s profile), installed unattended by servermgr on %s.\nCloud-init: Cloudbase-Init (Administrator password, hostname, network).",
				ed.Name, profile, time.Now().UTC().Format("2006-01-02"))},
		}
		logf("creating VM %d (%s): UEFI, TPM 2.0, VirtIO SCSI disk of %d GB on %s", vmid, name, req.DiskGB, d)
		var upid string
		if err := cl.Do(ctx, http.MethodPost, nodePath+"/qemu", cfg, &upid); err != nil {
			return 0, err
		}
		return vmid, cl.WaitTask(ctx, node, upid)
	}()
	if err != nil {
		return vmid, err
	}
	created(vmid)
	gp := proxmox.GuestPath(node, "qemu", vmid)
	logf("starting VM %d; Windows Setup runs unattended (watch it in the console)", vmid)
	if err := cl.Do(ctx, http.MethodPost, gp+"/status/start", nil, &upid); err != nil {
		return vmid, err
	}
	if err := cl.WaitTask(ctx, node, upid); err != nil {
		return vmid, err
	}
	// The installer CD asks to press a key before it boots.
	for i := 0; i < 20; i++ {
		_ = cl.Do(ctx, http.MethodPut, gp+"/sendkey", url.Values{"key": {"ret"}}, nil)
		select {
		case <-ctx.Done():
			return vmid, ctx.Err()
		case <-time.After(winPoll / 10):
		}
	}

	seen, sysprep := 0, false
	var last []string
	for {
		var fr struct {
			Content string `json:"content"`
		}
		if err := cl.Do(ctx, http.MethodGet, gp+"/agent/file-read", url.Values{"file": {winBuildLog}}, &fr); err == nil {
			lines := strings.Split(strings.TrimRight(strings.ReplaceAll(strings.TrimPrefix(fr.Content, "\ufeff"), "\r", ""), "\n"), "\n")
			if seen == 0 && len(lines) > 0 {
				logf("Windows is installed; the prepare script is running")
			}
			for _, l := range lines[min(seen, len(lines)):] {
				if l = strings.TrimSpace(l); l != "" {
					logf("windows: %s", l)
					sysprep = sysprep || strings.Contains(l, "SERVERMGR-SYSPREP")
					last = append(last, l)
				}
			}
			seen = max(seen, len(lines))
		}
		var st struct {
			Status string `json:"status"`
		}
		if err := cl.Do(ctx, http.MethodGet, gp+"/status/current", nil, &st); err != nil {
			return vmid, err
		}
		if st.Status == "stopped" {
			break
		}
		select {
		case <-ctx.Done():
			return vmid, fmt.Errorf("Windows did not finish in time; VM %d is left running to look at", vmid)
		case <-time.After(winPoll):
		}
	}
	if !sysprep {
		if len(last) > 3 {
			last = last[len(last)-3:]
		}
		return vmid, fmt.Errorf("VM %d stopped before Sysprep ran; it is kept to look at (last log lines: %s)", vmid, strings.Join(last, " | "))
	}
	logf("Sysprep finished and the VM shut down")
	return vmid, winFinish(ctx, cl, node, vmid, req.DiskStorage, profile, logf)
}

// winFinish turns a sysprepped, stopped Windows VM into a cloud-init
// template: CDs out, a cloud-init drive in (config drive format, which
// Cloudbase-Init reads), then the template conversion.
func winFinish(ctx context.Context, cl *proxmox.Client, node string, vmid int, ciStorage, profile string, logf func(string, ...any)) error {
	gp := proxmox.GuestPath(node, "qemu", vmid)
	var cfg map[string]any
	if err := cl.Do(ctx, http.MethodGet, gp+"/config", nil, &cfg); err != nil {
		return err
	}
	var drop []string
	for _, k := range []string{"ide0", "ide1", "ide2", "ide3", "sata4", "sata5"} {
		if v, ok := cfg[k].(string); ok && strings.Contains(v, "media=cdrom") {
			drop = append(drop, k)
		}
	}
	if len(drop) > 0 {
		logf("removing the CD drives (%s)", strings.Join(drop, ", "))
		if err := cl.Do(ctx, http.MethodPut, gp+"/config", url.Values{"delete": {strings.Join(drop, ",")}}, nil); err != nil {
			return err
		}
	}
	slot := "ide2"
	for _, k := range []string{"ide0", "ide1", "ide2", "ide3"} {
		if v, ok := cfg[k].(string); ok && strings.Contains(v, "cloudinit") {
			slot = ""
		}
	}
	tags := map[string]bool{"servermgr": true, "windows": true}
	if profile != "" {
		tags["win-"+profile] = true
	}
	if t, ok := cfg["tags"].(string); ok {
		for _, x := range strings.FieldsFunc(t, func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
			tags[x] = true
		}
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	set := url.Values{"citype": {"configdrive2"}, "ciuser": {"Administrator"}, "ipconfig0": {"ip=dhcp"}, "agent": {"enabled=1"}, "boot": {"order=scsi0"}, "tags": {strings.Join(keys, ";")}}
	if slot != "" {
		set.Set(slot, ciStorage+":cloudinit")
		logf("adding a cloud-init drive on %s", ciStorage)
	}
	if err := cl.Do(ctx, http.MethodPut, gp+"/config", set, nil); err != nil {
		return err
	}
	logf("converting VM %d to a template", vmid)
	var upid string
	if err := cl.Do(ctx, http.MethodPost, gp+"/template", nil, &upid); err != nil {
		return err
	}
	if upid != "" {
		return cl.WaitTask(ctx, node, upid)
	}
	return nil
}

func isWindowsConfig(cfg map[string]any) bool {
	t, _ := cfg["ostype"].(string)
	return strings.HasPrefix(t, "w")
}

// POST .../guests/{node}/qemu/{vmid}/windows-prep {storage}: puts the
// prepare script on a CD in the VM.
func (a *API) handlePVEWindowsPrep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Storage string `json:"storage"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusForbidden, "templates are made by the platform's administrators")
		return
	}
	if typ != "qemu" || !pveStorage.MatchString(req.Storage) {
		writeErr(w, http.StatusBadRequest, "pick an ISO storage")
		return
	}
	ctx := r.Context()
	cfg, err := guestConfig(ctx, s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !isWindowsConfig(cfg) {
		writeErr(w, http.StatusBadRequest, "this VM's OS type is not Windows")
		return
	}
	iso, err := prepISO()
	if err != nil {
		a.internal(w, err)
		return
	}
	file := fmt.Sprintf("servermgr-prepare-%d.iso", vmid)
	volid := req.Storage + ":iso/" + file
	np := "/nodes/" + url.PathEscape(node) + "/storage/" + url.PathEscape(req.Storage)
	if ok, _ := isoOnStorage(ctx, s.Cl, node, volid); ok {
		var upid string
		_ = s.Cl.Do(ctx, http.MethodDelete, np+"/content/"+url.PathEscape(volid), nil, &upid)
		if upid != "" {
			_ = s.Cl.WaitTask(ctx, node, upid)
		}
	}
	upid, err := s.Cl.Upload(ctx, np+"/upload", map[string]string{"content": "iso"}, file, bytes.NewReader(iso), int64(len(iso)))
	if err == nil {
		err = s.Cl.WaitTask(ctx, node, upid)
	}
	if err != nil {
		a.audit(r, "", "proxmox.windows_prep", fmt.Sprintf("%s/%d", s.C.Name, vmid), map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, "upload: "+err.Error())
		return
	}
	// A CD drive that exists can change its disc while the VM runs; a new
	// IDE drive only appears after a restart.
	slot, pending := "", false
	for _, k := range []string{"ide3", "ide2", "ide0", "ide1"} {
		if v, ok := cfg[k].(string); ok && strings.Contains(v, "media=cdrom") && !strings.Contains(v, "virtio-win") {
			slot = k
			break
		}
	}
	if slot == "" {
		for _, k := range []string{"ide3", "ide1", "ide0", "ide2"} {
			if _, used := cfg[k]; !used {
				slot, pending = k, true
				break
			}
		}
	}
	if slot == "" {
		writeErr(w, http.StatusConflict, "no free IDE slot for the CD; remove a drive first")
		return
	}
	if err := s.Cl.Do(ctx, http.MethodPut, proxmox.GuestPath(node, typ, vmid)+"/config", url.Values{slot: {volid + ",media=cdrom"}}, nil); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.windows_prep", fmt.Sprintf("%s/%d", s.C.Name, vmid), map[string]string{"cd": volid, "slot": slot}, true)
	writeJSON(w, http.StatusOK, map[string]any{"volid": volid, "slot": slot, "pending": pending})
}

// POST .../guests/{node}/qemu/{vmid}/windows-template {confirm, profile}:
// makes a sysprepped VM a cloud-init template.
func (a *API) handlePVEWindowsTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
		Profile string `json:"profile"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusForbidden, "templates are made by the platform's administrators")
		return
	}
	switch {
	case typ != "qemu":
		writeErr(w, http.StatusBadRequest, "not a VM")
		return
	case req.Confirm != strconv.Itoa(vmid):
		writeErr(w, http.StatusBadRequest, "type the VMID to confirm")
		return
	case req.Profile != "" && !slices.Contains(winProfiles, req.Profile):
		writeErr(w, http.StatusBadRequest, "profile: standard or hardened")
		return
	}
	ctx := r.Context()
	cfg, err := guestConfig(ctx, s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !isWindowsConfig(cfg) {
		writeErr(w, http.StatusBadRequest, "this VM's OS type is not Windows")
		return
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := s.Cl.Do(ctx, http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/status/current", nil, &st); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if st.Status != "stopped" {
		writeErr(w, http.StatusConflict, "run the prepare script first; Sysprep shuts the VM down")
		return
	}
	// The cloud-init drive goes where the system disk is.
	ciStorage := ""
	for _, k := range []string{"scsi0", "virtio0", "sata0", "ide0"} {
		if v, ok := cfg[k].(string); ok && !strings.Contains(v, "media=cdrom") {
			ciStorage, _, _ = strings.Cut(v, ":")
			break
		}
	}
	if !pveStorage.MatchString(ciStorage) {
		writeErr(w, http.StatusBadRequest, "cannot tell where the system disk is")
		return
	}
	var log []string
	logf := func(f string, args ...any) { log = append(log, fmt.Sprintf(f, args...)) }
	err = winFinish(ctx, s.Cl, node, vmid, ciStorage, req.Profile, logf)
	if err == nil {
		// The prepare CD is not needed any more.
		for _, v := range cfg {
			vs, _ := v.(string)
			if vol, _, _ := strings.Cut(vs, ","); strings.HasSuffix(vol, fmt.Sprintf("/servermgr-prepare-%d.iso", vmid)) {
				st, _, _ := strings.Cut(vol, ":")
				var upid string
				_ = s.Cl.Do(ctx, http.MethodDelete, "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(st)+"/content/"+url.PathEscape(vol), nil, &upid)
			}
		}
	}
	target := fmt.Sprintf("%s/%d", s.C.Name, vmid)
	if err != nil {
		a.audit(r, "", "proxmox.windows_template", target, map[string]string{"error": err.Error()}, false)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	a.audit(r, "", "proxmox.windows_template", target, map[string]any{"steps": log}, true)
	writeJSON(w, http.StatusOK, map[string]any{"log": log})
}

var errWinEnroll = errors.New("the servermgr agent runs on Linux; deploy Windows without enrolling")

// winUnlockAdmin enables the built-in Administrator (Sysprep disables it
// on Windows 10/11) and clears a lockout. It runs through the guest agent
// as SYSTEM and takes no input.
var winUnlockAdmin = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
	`$a = Get-LocalUser | Where-Object { $_.SID -like '*-500' }; Enable-LocalUser -SID $a.SID; ` +
		`$u = [ADSI]('WinNT://./' + $a.Name + ',user'); $u.IsAccountLocked = $false; $u.SetInfo()`}

// POST /api/v1/proxmox/{id}/guests/{node}/qemu/{vmid}/windows-admin resets
// a Windows VM's Administrator through the QEMU guest agent: enables the
// account, clears a lockout and sets a new password. The password goes
// through Proxmox's set-user-password, never onto a command line.
func (a *API) handlePVEWindowsAdmin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if msg := winPasswordProblem(req.Password); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if typ != "qemu" {
		writeErr(w, http.StatusBadRequest, "only Windows VMs have an Administrator")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	cfg, err := guestConfig(ctx, s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !isWindowsConfig(cfg) {
		writeErr(w, http.StatusBadRequest, "this VM's OS type is not Windows")
		return
	}
	target := fmt.Sprintf("%s/%s/%d", s.C.Name, node, vmid)
	fail := func(step string, err error) {
		a.audit(r, "", "proxmox.windows_admin_reset", target, map[string]string{"error": step + ": " + err.Error()}, false)
		var ae *proxmox.APIError
		if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Msg), "not running") {
			err = errors.New("the guest agent is not running; start the VM and wait until Windows has booted")
		}
		writeErr(w, http.StatusBadGateway, step+": "+err.Error())
	}
	gp := proxmox.GuestPath(node, typ, vmid)
	var ex struct {
		PID int `json:"pid"`
	}
	if err := s.Cl.Do(ctx, http.MethodPost, gp+"/agent/exec", url.Values{"command": winUnlockAdmin}, &ex); err != nil {
		fail("unlocking Administrator", err)
		return
	}
	for {
		var st struct {
			Exited   int    `json:"exited"`
			ExitCode int    `json:"exitcode"`
			ErrData  string `json:"err-data"`
		}
		if err := s.Cl.Do(ctx, http.MethodGet, gp+"/agent/exec-status", url.Values{"pid": {strconv.Itoa(ex.PID)}}, &st); err != nil {
			fail("unlocking Administrator", err)
			return
		}
		if st.Exited == 1 {
			if st.ExitCode != 0 {
				fail("unlocking Administrator", fmt.Errorf("exit code %d: %s", st.ExitCode, cleanText(st.ErrData, 200)))
				return
			}
			break
		}
		select {
		case <-ctx.Done():
			fail("unlocking Administrator", ctx.Err())
			return
		case <-time.After(time.Second):
		}
	}
	if err := s.Cl.Do(ctx, http.MethodPost, gp+"/agent/set-user-password", url.Values{"username": {"Administrator"}, "password": {req.Password}}, nil); err != nil {
		fail("setting the password", err)
		return
	}
	a.audit(r, "", "proxmox.windows_admin_reset", target, nil, true)
	w.WriteHeader(http.StatusNoContent)
}
