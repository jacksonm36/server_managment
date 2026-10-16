package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Guest hardware and options: CPU, memory, disks, network cards, CD drive,
// boot order, name, notes and tags. Every change is a typed field checked
// here and turned into a Proxmox config key; nothing is passed through raw,
// so keys that reach the host (args, hookscript, hostpci, bind mounts) are
// never settable. Tenants are also held to their storages, bridges and
// quota.

var (
	pveDiskKey   = regexp.MustCompile(`^(scsi|virtio|sata|ide)\d{1,2}$|^(rootfs|mp\d{1,3})$`)
	pveNetKey    = regexp.MustCompile(`^net\d{1,2}$`)
	pveTag       = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.+-]{0,62}$`)
	pveCPUType   = regexp.MustCompile(`^(host|kvm64|qemu64|x86-64-v[2-4](-AES)?|max)$`)
	pveIfName    = regexp.MustCompile(`^eth\d{1,2}$`)
	pveBootDev   = regexp.MustCompile(`^((scsi|virtio|sata|ide)\d{1,2}|net\d{1,2})$`)
	pveNICModels = []string{"virtio", "e1000", "e1000e", "vmxnet3", "rtl8139"}
	pveDiskBuses = map[string]int{"scsi": 30, "virtio": 15, "sata": 5}
)

type pveHardwareOpts struct {
	Storages []string `json:"storages"` // disk storages on the guest's node
	Bridges  []string `json:"bridges"`
	ISOs     []string `json:"isos"`
}

// guestConfig reads the guest's current config.
func guestConfig(ctx context.Context, s *pveScope, node, typ string, vmid int) (map[string]any, error) {
	var cfg map[string]any
	err := s.Cl.Do(ctx, http.MethodGet, proxmox.GuestPath(node, typ, vmid)+"/config", nil, &cfg)
	return cfg, err
}

// contentStorages lists active storages on node holding content that the
// scope may use.
func contentStorages(ctx context.Context, s *pveScope, node, content string) ([]string, error) {
	var list []pveStorageInfo
	if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage", url.Values{"content": {content}}, &list); err != nil {
		return nil, err
	}
	out := []string{}
	for _, st := range list {
		if st.Active == 0 || (s.TC != nil && !slices.Contains(s.TC.Storages, st.Storage)) {
			continue
		}
		out = append(out, st.Storage)
	}
	slices.Sort(out)
	return out, nil
}

func nodeBridges(ctx context.Context, s *pveScope, node string) ([]string, error) {
	if s.TC != nil {
		return slices.Clone(s.TC.Bridges), nil
	}
	var ifs []struct {
		Iface string `json:"iface"`
	}
	if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/network", url.Values{"type": {"any_bridge"}}, &ifs); err != nil {
		return nil, err
	}
	out := []string{}
	for _, i := range ifs {
		out = append(out, i.Iface)
	}
	slices.Sort(out)
	return out, nil
}

func nodeISOs(ctx context.Context, s *pveScope, node string) ([]string, error) {
	storages, err := contentStorages(ctx, s, node, "iso")
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, st := range storages {
		var list []struct {
			VolID string `json:"volid"`
		}
		if err := s.Cl.Do(ctx, http.MethodGet, "/nodes/"+url.PathEscape(node)+"/storage/"+url.PathEscape(st)+"/content", url.Values{"content": {"iso"}}, &list); err != nil {
			return nil, err
		}
		for _, v := range list {
			if strings.HasPrefix(v.VolID, st+":") {
				out = append(out, v.VolID)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// handlePVEHardwareOptions returns what the hardware editor may choose from.
func (a *API) handlePVEHardwareOptions(w http.ResponseWriter, r *http.Request) {
	s, node, typ, _, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	var out pveHardwareOpts
	var err error
	if out.Storages, err = contentStorages(r.Context(), s, node, map[string]string{"qemu": "images", "lxc": "rootdir"}[typ]); err == nil {
		if out.Bridges, err = nodeBridges(r.Context(), s, node); err == nil && typ == "qemu" {
			out.ISOs, err = nodeISOs(r.Context(), s, node)
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if out.ISOs == nil {
		out.ISOs = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

// guestQuotaCheck says whether growing a tenant's guest by add stays within
// the tenant's quota ("" when it does, or for platform users).
func (a *API) guestQuotaCheck(r *http.Request, s *pveScope, add tenantUsage) string {
	if s.TC == nil || (add.Cores <= 0 && add.MemoryMB <= 0 && add.DiskGB <= 0) {
		return ""
	}
	res, err := s.Cl.Resources(r.Context())
	if err != nil {
		return "cannot read your guests: " + err.Error()
	}
	a.pveMu.Lock()
	total := poolUsage(res, s.TC.Pool)
	total.add(a.pveReserved[s.TC.TenantID])
	a.pveMu.Unlock()
	total.add(add)
	if limit := overQuota(accessFrom(r.Context()).Tenant, total); limit != "" {
		return "this would go over your limit of " + limit
	}
	return ""
}

// configCall writes guest config: async POST for VMs, PUT for containers.
func (a *API) configCall(w http.ResponseWriter, r *http.Request, typ string, form url.Values, audit string) {
	method := http.MethodPost
	if typ == "lxc" {
		method = http.MethodPut
	}
	a.pveGuestCall(w, r, method, "/config", form, audit)
}

// ---- CPU, memory and options ----

type pveHardwareReq struct {
	Cores       *int      `json:"cores"`
	Sockets     *int      `json:"sockets"`
	CPU         *string   `json:"cpu"`
	CPULimit    *float64  `json:"cpulimit"`
	MemoryMB    *int      `json:"memory"`
	BalloonMB   *int      `json:"balloon"` // VM minimum memory; 0 turns ballooning off
	SwapMB      *int      `json:"swap"`    // containers
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Tags        *[]string `json:"tags"`
	OnBoot      *bool     `json:"onboot"`
	Protection  *bool     `json:"protection"`
	Agent       *bool     `json:"agent"`
	Boot        *[]string `json:"boot"`
}

func b01(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (a *API) handlePVEHardware(w http.ResponseWriter, r *http.Request) {
	var req pveHardwareReq
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	cur := func(k string, def int) int {
		if n, err := strconv.Atoi(cfgStr(cfg, k)); err == nil {
			return n
		}
		return def
	}
	form := url.Values{}
	bad := func(msg string) { writeErr(w, http.StatusBadRequest, msg) }
	oldCPU := cur("cores", 1) * cur("sockets", 1)
	newCores, newSockets := cur("cores", 1), cur("sockets", 1)
	if req.Cores != nil {
		if *req.Cores < 1 || *req.Cores > 512 {
			bad("cores: 1-512")
			return
		}
		newCores = *req.Cores
		form.Set("cores", strconv.Itoa(*req.Cores))
	}
	if req.Sockets != nil {
		if typ != "qemu" || *req.Sockets < 1 || *req.Sockets > 8 {
			bad("sockets: 1-8, VMs only")
			return
		}
		newSockets = *req.Sockets
		form.Set("sockets", strconv.Itoa(*req.Sockets))
	}
	if req.CPU != nil {
		if typ != "qemu" || !pveCPUType.MatchString(*req.CPU) {
			bad("cpu: host, kvm64, qemu64, x86-64-v2/v3/v4 (-AES) or max")
			return
		}
		form.Set("cpu", *req.CPU)
	}
	if req.CPULimit != nil {
		if *req.CPULimit < 0 || *req.CPULimit > 512 {
			bad("cpu limit: 0 (none) to 512")
			return
		}
		form.Set("cpulimit", strconv.FormatFloat(*req.CPULimit, 'f', -1, 64))
	}
	oldMem := cur("memory", 512)
	newMem := oldMem
	if req.MemoryMB != nil {
		if *req.MemoryMB < 16 || *req.MemoryMB > 4<<20 {
			bad("memory: 16 MiB to 4 TiB")
			return
		}
		newMem = *req.MemoryMB
		form.Set("memory", strconv.Itoa(*req.MemoryMB))
	}
	if req.BalloonMB != nil {
		if typ != "qemu" || *req.BalloonMB < 0 || *req.BalloonMB > newMem {
			bad("minimum memory: 0 (ballooning off) up to the memory size, VMs only")
			return
		}
		form.Set("balloon", strconv.Itoa(*req.BalloonMB))
	}
	if req.SwapMB != nil {
		if typ != "lxc" || *req.SwapMB < 0 || *req.SwapMB > 1<<20 {
			bad("swap: 0-1048576 MiB, containers only")
			return
		}
		form.Set("swap", strconv.Itoa(*req.SwapMB))
	}
	if req.Name != nil {
		if !pveHost.MatchString(*req.Name) {
			bad("name: lowercase letters, digits and hyphens")
			return
		}
		form.Set(map[string]string{"qemu": "name", "lxc": "hostname"}[typ], *req.Name)
	}
	if req.Description != nil {
		if len(*req.Description) > 8192 {
			bad("notes: at most 8 KiB")
			return
		}
		form.Set("description", *req.Description)
	}
	if req.Tags != nil {
		if len(*req.Tags) > 20 {
			bad("at most 20 tags")
			return
		}
		for _, t := range *req.Tags {
			if !pveTag.MatchString(t) {
				bad("tags: lowercase letters, digits, _ . + -")
				return
			}
		}
		form.Set("tags", strings.Join(*req.Tags, ";"))
	}
	if req.OnBoot != nil {
		form.Set("onboot", b01(*req.OnBoot))
	}
	if req.Protection != nil {
		form.Set("protection", b01(*req.Protection))
	}
	if req.Agent != nil {
		if typ != "qemu" {
			bad("the guest agent is for VMs")
			return
		}
		form.Set("agent", b01(*req.Agent))
	}
	if req.Boot != nil {
		if typ != "qemu" || len(*req.Boot) == 0 || len(*req.Boot) > 10 {
			bad("boot order: 1-10 devices, VMs only")
			return
		}
		for _, d := range *req.Boot {
			if !pveBootDev.MatchString(d) || cfgStr(cfg, d) == "" {
				bad("boot order: " + d + " is not a device of this VM")
				return
			}
		}
		form.Set("boot", "order="+strings.Join(*req.Boot, ";"))
	}
	if len(form) == 0 {
		bad("nothing to change")
		return
	}
	if msg := a.guestQuotaCheck(r, s, tenantUsage{Cores: newCores*newSockets - oldCPU, MemoryMB: int64(newMem - oldMem)}); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	a.configCall(w, r, typ, form, "proxmox.hardware")
}

// cfgStr reads a config value as a string.
func cfgStr(cfg map[string]any, k string) string {
	switch v := cfg[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// ---- disks ----

func isCDROM(v string) bool { return strings.Contains(v, "media=cdrom") }

func (a *API) handlePVEDiskAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Bus     string `json:"bus"` // VMs: scsi, virtio or sata
		Storage string `json:"storage"`
		SizeGB  int    `json:"size_gb"`
		Path    string `json:"path"` // containers: mount point
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if req.SizeGB < 1 || req.SizeGB > 65536 {
		writeErr(w, http.StatusBadRequest, "size: 1-65536 GB")
		return
	}
	storages, err := contentStorages(r.Context(), s, node, map[string]string{"qemu": "images", "lxc": "rootdir"}[typ])
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !slices.Contains(storages, req.Storage) {
		writeErr(w, http.StatusBadRequest, "pick one of the listed storages")
		return
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var key, val string
	if typ == "qemu" {
		max, ok := pveDiskBuses[req.Bus]
		if !ok {
			writeErr(w, http.StatusBadRequest, "bus: scsi, virtio or sata")
			return
		}
		for i := 0; i <= max; i++ {
			if _, used := cfg[fmt.Sprintf("%s%d", req.Bus, i)]; !used {
				key = fmt.Sprintf("%s%d", req.Bus, i)
				break
			}
		}
		val = fmt.Sprintf("%s:%d", req.Storage, req.SizeGB)
		if req.Bus != "sata" {
			val += ",discard=on"
		}
	} else {
		if !strings.HasPrefix(req.Path, "/") || strings.Contains(req.Path, "..") || strings.ContainsAny(req.Path, ",= \n") || len(req.Path) > 200 || req.Path == "/" {
			writeErr(w, http.StatusBadRequest, "mount point: an absolute path like /data")
			return
		}
		for i := 0; i < 256; i++ {
			if _, used := cfg[fmt.Sprintf("mp%d", i)]; !used {
				key = fmt.Sprintf("mp%d", i)
				break
			}
		}
		// storage:size makes a new volume; a host path here would be a bind
		// mount, which the storage check above already rules out.
		val = fmt.Sprintf("%s:%d,mp=%s", req.Storage, req.SizeGB, req.Path)
	}
	if key == "" {
		writeErr(w, http.StatusBadRequest, "no free slot on that bus")
		return
	}
	if msg := a.guestQuotaCheck(r, s, tenantUsage{DiskGB: int64(req.SizeGB)}); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	a.configCall(w, r, typ, url.Values{key: {val}}, "proxmox.disk_add")
}

func (a *API) handlePVEDiskResize(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk   string `json:"disk"`
		GrowGB int    `json:"grow_gb"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if req.GrowGB < 1 || req.GrowGB > 65536 {
		writeErr(w, http.StatusBadRequest, "grow by 1-65536 GB (disks can only grow)")
		return
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if v := cfgStr(cfg, req.Disk); !pveDiskKey.MatchString(req.Disk) || v == "" || isCDROM(v) {
		writeErr(w, http.StatusBadRequest, "not a disk of this guest")
		return
	}
	if msg := a.guestQuotaCheck(r, s, tenantUsage{DiskGB: int64(req.GrowGB)}); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	a.pveGuestCall(w, r, http.MethodPut, "/resize", url.Values{"disk": {req.Disk}, "size": {fmt.Sprintf("+%dG", req.GrowGB)}}, "proxmox.disk_resize")
}

// handlePVEDiskRemove detaches a disk (it becomes "unused", data kept) or,
// with destroy and the disk's name as confirmation, deletes its volume.
func (a *API) handlePVEDiskRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk    string `json:"disk"`
		Destroy bool   `json:"destroy"`
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	unused := regexp.MustCompile(`^unused\d{1,3}$`).MatchString(req.Disk)
	v := cfgStr(cfg, req.Disk)
	if v == "" || isCDROM(v) || req.Disk == "rootfs" || (!unused && !pveDiskKey.MatchString(req.Disk)) {
		writeErr(w, http.StatusBadRequest, "not a removable disk of this guest")
		return
	}
	if !req.Destroy {
		if unused {
			writeErr(w, http.StatusBadRequest, "that disk is already detached")
			return
		}
		a.configCall(w, r, typ, url.Values{"delete": {req.Disk}}, "proxmox.disk_detach")
		return
	}
	if req.Confirm != req.Disk {
		writeErr(w, http.StatusBadRequest, "type the disk's name to confirm deleting its data")
		return
	}
	if typ == "qemu" {
		a.pveGuestCall(w, r, http.MethodPut, "/unlink", url.Values{"idlist": {req.Disk}, "force": {"1"}}, "proxmox.disk_destroy")
		return
	}
	if !unused {
		writeErr(w, http.StatusBadRequest, "detach the mount point first, then delete it")
		return
	}
	a.configCall(w, r, typ, url.Values{"delete": {req.Disk}}, "proxmox.disk_destroy")
}

// ---- network cards ----

func (a *API) handlePVENIC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string  `json:"model"` // VMs
		Bridge   string  `json:"bridge"`
		VLAN     int     `json:"vlan"`
		Firewall bool    `json:"firewall"`
		RateMBps float64 `json:"rate"`
		MAC      string  `json:"mac"`
		Name     string  `json:"name"` // containers: eth0...
		IP       string  `json:"ip"`   // containers: dhcp, manual or CIDR
		GW       string  `json:"gw"`
		Down     bool    `json:"link_down"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	key := r.PathValue("nic")
	if !pveNetKey.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "bad network card")
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	bridges, err := nodeBridges(r.Context(), s, node)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if !slices.Contains(bridges, req.Bridge) {
		writeErr(w, http.StatusBadRequest, "pick one of the listed networks")
		return
	}
	if req.VLAN < 0 || req.VLAN > 4094 || req.RateMBps < 0 || req.RateMBps > 100000 {
		writeErr(w, http.StatusBadRequest, "VLAN 0-4094, rate 0-100000 MB/s")
		return
	}
	if req.MAC != "" {
		if hw, err := net.ParseMAC(req.MAC); err != nil || len(hw) != 6 {
			writeErr(w, http.StatusBadRequest, "bad MAC address")
			return
		}
	} else if cfg, err := guestConfig(r.Context(), s, node, typ, vmid); err == nil {
		// Keep the card's MAC when editing it.
		req.MAC = existingMAC(cfgStr(cfg, key))
	}
	var parts []string
	if typ == "qemu" {
		if !slices.Contains(pveNICModels, req.Model) {
			writeErr(w, http.StatusBadRequest, "model: "+strings.Join(pveNICModels, ", "))
			return
		}
		m := req.Model
		if req.MAC != "" {
			m += "=" + strings.ToUpper(req.MAC)
		}
		parts = append(parts, m, "bridge="+req.Bridge)
	} else {
		if !pveIfName.MatchString(req.Name) {
			writeErr(w, http.StatusBadRequest, "interface name: eth0-eth99")
			return
		}
		parts = append(parts, "name="+req.Name, "bridge="+req.Bridge)
		if req.MAC != "" {
			parts = append(parts, "hwaddr="+strings.ToUpper(req.MAC))
		}
		switch ip := req.IP; {
		case ip == "dhcp" || ip == "manual":
			parts = append(parts, "ip="+ip)
		case ip != "":
			if _, _, err := net.ParseCIDR(ip); err != nil || !strings.Contains(ip, ".") {
				writeErr(w, http.StatusBadRequest, "IPv4: dhcp, manual or an address like 10.0.0.5/24")
				return
			}
			parts = append(parts, "ip="+ip)
			if req.GW != "" {
				if g := net.ParseIP(req.GW); g == nil || g.To4() == nil {
					writeErr(w, http.StatusBadRequest, "bad gateway")
					return
				}
				parts = append(parts, "gw="+req.GW)
			}
		}
	}
	if req.VLAN > 0 {
		parts = append(parts, "tag="+strconv.Itoa(req.VLAN))
	}
	if req.Firewall {
		parts = append(parts, "firewall=1")
	}
	if req.RateMBps > 0 {
		parts = append(parts, "rate="+strconv.FormatFloat(req.RateMBps, 'f', -1, 64))
	}
	if req.Down {
		parts = append(parts, "link_down=1")
	}
	a.configCall(w, r, typ, url.Values{key: {strings.Join(parts, ",")}}, "proxmox.nic")
}

var pveMACRe = regexp.MustCompile(`(?i)(?:^|,)(?:virtio|e1000e?|vmxnet3|rtl8139|hwaddr)=([0-9a-f]{2}(?::[0-9a-f]{2}){5})`)

func existingMAC(v string) string {
	if m := pveMACRe.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return ""
}

func (a *API) handlePVENICDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("nic")
	if !pveNetKey.MatchString(key) {
		writeErr(w, http.StatusBadRequest, "bad network card")
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if cfgStr(cfg, key) == "" {
		writeErr(w, http.StatusNotFound, "no such network card")
		return
	}
	a.configCall(w, r, typ, url.Values{"delete": {key}}, "proxmox.nic_delete")
}

// ---- CD drive ----

func (a *API) handlePVECDROM(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ISO string `json:"iso"` // a volid from the options, or "" to eject
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, node, typ, vmid, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	if typ != "qemu" {
		writeErr(w, http.StatusBadRequest, "CD drives are for VMs")
		return
	}
	cfg, err := guestConfig(r.Context(), s, node, typ, vmid)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// Use the existing CD drive, else the first free IDE slot (ide2 first,
	// where Proxmox puts one).
	key := ""
	for k, v := range cfg {
		if sv, _ := v.(string); pveDiskKey.MatchString(k) && isCDROM(sv) && !strings.Contains(sv, "cloudinit") {
			key = k
			break
		}
	}
	for _, k := range []string{"ide2", "ide0", "ide1", "ide3"} {
		if key != "" {
			break
		}
		if cfgStr(cfg, k) == "" {
			key = k
		}
	}
	if key == "" {
		writeErr(w, http.StatusConflict, "no free IDE slot for a CD drive")
		return
	}
	val := "none,media=cdrom"
	if req.ISO != "" {
		isos, err := nodeISOs(r.Context(), s, node)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if !slices.Contains(isos, req.ISO) {
			writeErr(w, http.StatusBadRequest, "pick one of the listed ISO images")
			return
		}
		val = req.ISO + ",media=cdrom"
	}
	a.configCall(w, r, typ, url.Values{key: {val}}, "proxmox.cdrom")
}
