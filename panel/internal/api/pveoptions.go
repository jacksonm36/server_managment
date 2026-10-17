package api

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Guest options the Configuration tab edits directly. Each key says what
// it accepts. Keys that reach the host (args, hookscript, hostpci, lxc.*,
// mount points) are not here, so they stay unsettable from the panel;
// hardware has its own tab.

type pveOpt struct {
	Key    string   `json:"key"`
	Types  string   `json:"-"`                // "qemu", "lxc" or "" for both
	Kind   string   `json:"kind"`             // bool | enum | text
	Values []string `json:"values,omitempty"` // enum choices
	About  string   `json:"about"`
	Hint   string   `json:"hint,omitempty"`
	Admin  bool     `json:"admin,omitempty"`   // platform admins only
	Def    string   `json:"default,omitempty"` // what Proxmox uses when unset
	re     *regexp.Regexp
}

func boolOpt(key, types, about, def string) pveOpt {
	return pveOpt{Key: key, Types: types, Kind: "bool", About: about, Def: def, re: regexp.MustCompile(`^[01]$`)}
}

func enumOpt(key, types, about string, values ...string) pveOpt {
	return pveOpt{Key: key, Types: types, Kind: "enum", About: about, Values: values}
}

func textOpt(key, types, about, hint, re string) pveOpt {
	return pveOpt{Key: key, Types: types, Kind: "text", About: about, Hint: hint, re: regexp.MustCompile(re)}
}

var pveOptions = []pveOpt{
	enumOpt("ostype", "qemu", "Guest OS type (tunes defaults)", "l26", "l24", "win11", "win10", "win8", "win7", "wvista", "wxp", "w2k8", "w2k3", "w2k", "solaris", "other"),
	enumOpt("ostype", "lxc", "Container OS type", "debian", "ubuntu", "centos", "fedora", "opensuse", "archlinux", "alpine", "gentoo", "nixos", "unmanaged"),
	enumOpt("bios", "qemu", "Firmware", "seabios", "ovmf"),
	textOpt("machine", "qemu", "Machine type", "q35, pc, pc-q35-9.0, pc-i440fx-10.1", `^(pc|q35|pc-(i440fx|q35)-\d+\.\d+)(\+pve\d+)?(,viommu=(intel|virtio))?$`),
	enumOpt("scsihw", "qemu", "SCSI controller", "virtio-scsi-single", "virtio-scsi-pci", "lsi", "lsi53c810", "megasas", "pvscsi"),
	textOpt("vga", "qemu", "Display (,clipboard=vnc shares the clipboard with the console)", "std,clipboard=vnc or virtio, serial0, none", `^(std|cirrus|vmware|qxl|qxl[2-4]|virtio|virtio-gl|serial[0-3]|none)(,(memory=\d{1,4}|clipboard=vnc)){0,2}$`),
	textOpt("agent", "qemu", "QEMU guest agent", "1, or enabled=1,fstrim_cloned_disks=1", `^([01]|enabled=[01](,fstrim_cloned_disks=[01])?(,freeze-fs-on-backup=[01])?(,type=(virtio|isa))?)$`),
	textOpt("hotplug", "qemu", "Hot-plug", "network,disk,usb (or 0 / 1)", `^([01]|(disk|network|usb|memory|cpu|cloudinit)(,(disk|network|usb|memory|cpu|cloudinit))*)$`),
	boolOpt("numa", "qemu", "NUMA", "0"),
	boolOpt("kvm", "qemu", "KVM hardware virtualization", "1"),
	boolOpt("acpi", "qemu", "ACPI", "1"),
	boolOpt("tablet", "qemu", "USB tablet pointer (exact mouse in the console)", "1"),
	boolOpt("localtime", "qemu", "Real-time clock in local time (Windows)", "0"),
	boolOpt("freeze", "qemu", "Freeze CPU at start", "0"),
	boolOpt("allow-ksm", "qemu", "Allow kernel same-page merging", "1"),
	boolOpt("reboot", "qemu", "Allow reboot (0 makes a reboot stop the VM)", "1"),
	boolOpt("ciupgrade", "qemu", "Upgrade packages on first cloud-init boot", "1"),
	textOpt("keyboard", "qemu", "Console keyboard layout", "en-us, de, hu, fr…", `^[a-z]{2}(-[a-z]{2,3})?$`),
	boolOpt("onboot", "", "Start when the node boots", "0"),
	boolOpt("protection", "", "Protection (blocks delete and disk removal)", "0"),
	textOpt("startup", "", "Start order and delays", "order=1,up=30,down=60", `^(order=\d{1,4}|up=\d{1,5}|down=\d{1,5})(,(order=\d{1,4}|up=\d{1,5}|down=\d{1,5})){0,2}$`),
	boolOpt("console", "lxc", "Console device", "1"),
	textOpt("tty", "lxc", "TTYs", "0-6", `^[0-6]$`),
	enumOpt("cmode", "lxc", "Console mode", "tty", "console", "shell"),
	textOpt("timezone", "lxc", "Time zone", "host, or Europe/Budapest", `^(host|[A-Za-z_]+(/[A-Za-z0-9_+-]+){0,2})$`),
	textOpt("nameserver", "lxc", "DNS servers", "1.1.1.1 9.9.9.9", `^[0-9a-fA-F.:]+( [0-9a-fA-F.:]+){0,2}$`),
	textOpt("searchdomain", "lxc", "Search domain", "lan.example.com", `^[A-Za-z0-9.-]+( [A-Za-z0-9.-]+){0,5}$`),
	{Key: "features", Types: "lxc", Kind: "text", About: "Container features", Hint: "nesting=1,keyctl=1", Admin: true,
		re: regexp.MustCompile(`^(nesting|keyctl|fuse|mknod)=[01](,(nesting|keyctl|fuse|mknod)=[01]){0,3}$`)},
}

func optionsFor(typ string) []pveOpt {
	var out []pveOpt
	for _, o := range pveOptions {
		if o.Types == "" || o.Types == typ {
			out = append(out, o)
		}
	}
	return out
}

func (a *API) handlePVEOptionsList(w http.ResponseWriter, r *http.Request) {
	typ := r.PathValue("type")
	if typ != "qemu" && typ != "lxc" {
		writeErr(w, http.StatusNotFound, "not a guest")
		return
	}
	writeJSON(w, http.StatusOK, optionsFor(typ))
}

// handlePVEOptions sets and resets options from pveOptions.
func (a *API) handlePVEOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Set    map[string]string `json:"set"`
		Delete []string          `json:"delete"` // back to the default
	}
	if !readJSON(w, r, &req) {
		return
	}
	s, _, typ, _, ok := a.pveGuest(w, r)
	if !ok {
		return
	}
	known := map[string]pveOpt{}
	for _, o := range optionsFor(typ) {
		known[o.Key] = o
	}
	admin := s.TC == nil && accessFrom(r.Context()).Can("proxmox.manage")
	check := func(k string) (pveOpt, string) {
		o, found := known[k]
		switch {
		case !found:
			return o, k + " can't be set here"
		case o.Admin && !admin:
			return o, k + " is for platform admins"
		}
		return o, ""
	}
	form := url.Values{}
	keys := make([]string, 0, len(req.Set))
	for k := range req.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := strings.TrimSpace(req.Set[k])
		o, msg := check(k)
		if msg == "" {
			switch {
			case o.Kind == "enum" && !slices.Contains(o.Values, v):
				msg = k + ": one of " + strings.Join(o.Values, ", ")
			case o.Kind != "enum" && !o.re.MatchString(v):
				msg = k + ": " + map[bool]string{true: "use 0 or 1", false: "use a value like " + o.Hint}[o.Kind == "bool"]
			}
		}
		if msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		form.Set(k, v)
	}
	var del []string
	for _, k := range req.Delete {
		if _, msg := check(k); msg != "" {
			writeErr(w, http.StatusBadRequest, msg)
			return
		}
		if _, both := req.Set[k]; both {
			writeErr(w, http.StatusBadRequest, k+": set or reset, not both")
			return
		}
		del = append(del, k)
	}
	if len(del) > 0 {
		form.Set("delete", strings.Join(del, ","))
	}
	if len(form) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to change")
		return
	}
	a.configCall(w, r, typ, form, "proxmox.options")
}
