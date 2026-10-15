package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jacksonm36/servermgr/panel/internal/proxmox"
)

// Ready-made OpenTofu stacks for a Proxmox cluster (bpg/proxmox provider).
// A workspace made from one is tied to the cluster, so the provider block
// stays empty: servermgr supplies the endpoint and token on each run.
// Variable defaults are filled in from the cluster (node, storages,
// bridge, a template, the panel's SSH key).

type tofuStack struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Summary string            `json:"summary"`
	Files   map[string]string `json:"files"`
}

const stackProvider = `terraform {
  required_providers {
    proxmox = {
      source  = "bpg/proxmox"
      version = "~> 0.116"
    }
  }
}

# servermgr sets the endpoint and API token for this workspace's cluster.
provider "proxmox" {}
`

const stackClusterVars = `variable "node" {
  type    = string
  default = "__NODE__"
}

variable "disk_storage" {
  type    = string
  default = "__DISK_STORAGE__"
}

variable "bridge" {
  type    = string
  default = "__BRIDGE__"
}

variable "ssh_keys" {
  description = "Public keys for the default user (the panel's key lets servermgr reach the guest)."
  type        = list(string)
  default     = ["__SSH_KEY__"]
}
`

var tofuStacks = []tofuStack{
	{
		ID:      "cloud-vms",
		Name:    "Cloud-init VMs from an official image",
		Summary: "Downloads a Debian 13 cloud image into the node's import storage and creates a number of VMs from it, with DHCP, the guest agent and your SSH keys.",
		Files: map[string]string{
			"main.tf": stackProvider + "\n" + stackClusterVars + `
variable "image_storage" {
  description = "A storage with the Import content type."
  type        = string
  default     = "__IMAGE_STORAGE__"
}

variable "image_url" {
  type    = string
  default = "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
}

variable "name_prefix" {
  type    = string
  default = "web"
}

variable "vm_count" {
  type    = number
  default = 2
}

variable "cores" {
  type    = number
  default = 2
}

variable "memory_mb" {
  type    = number
  default = 2048
}

variable "disk_gb" {
  type    = number
  default = 20
}

resource "proxmox_download_file" "image" {
  content_type = "import"
  datastore_id = var.image_storage
  node_name    = var.node
  url          = var.image_url
  file_name    = "tofu-${basename(var.image_url)}"
  overwrite    = false
}

resource "proxmox_virtual_environment_vm" "vm" {
  count     = var.vm_count
  name      = "${var.name_prefix}-${format("%02d", count.index + 1)}"
  node_name = var.node
  tags      = ["tofu"]

  agent {
    enabled = true
  }
  stop_on_destroy = true

  cpu {
    cores = var.cores
    type  = "x86-64-v2-AES"
  }

  memory {
    dedicated = var.memory_mb
  }

  disk {
    datastore_id = var.disk_storage
    import_from  = proxmox_download_file.image.id
    interface    = "scsi0"
    size         = var.disk_gb
    discard      = "on"
    iothread     = true
  }
  scsi_hardware = "virtio-scsi-single"

  initialization {
    datastore_id = var.disk_storage
    ip_config {
      ipv4 {
        address = "dhcp"
      }
    }
    user_account {
      username = "debian"
      keys     = var.ssh_keys
    }
  }

  network_device {
    bridge = var.bridge
  }

  operating_system {
    type = "l26"
  }

  serial_device {}
}

output "vms" {
  value = { for v in proxmox_virtual_environment_vm.vm : v.name => v.ipv4_addresses }
}
`,
		},
	},
	{
		ID:      "clone-template",
		Name:    "VMs cloned from a template",
		Summary: "Full clones of an existing VM template (for example one made with From image), resized and given your SSH keys through cloud-init.",
		Files: map[string]string{
			"main.tf": stackProvider + "\n" + stackClusterVars + `
variable "template_vmid" {
  type    = number
  default = __TEMPLATE_VMID__
}

variable "names" {
  type    = list(string)
  default = ["app-01"]
}

variable "cores" {
  type    = number
  default = 2
}

variable "memory_mb" {
  type    = number
  default = 4096
}

variable "disk_gb" {
  type    = number
  default = 32
}

resource "proxmox_virtual_environment_vm" "vm" {
  for_each  = toset(var.names)
  name      = each.key
  node_name = var.node
  tags      = ["tofu"]

  clone {
    vm_id        = var.template_vmid
    full         = true
    datastore_id = var.disk_storage
  }

  agent {
    enabled = true
  }
  stop_on_destroy = true

  cpu {
    cores = var.cores
  }

  memory {
    dedicated = var.memory_mb
  }

  disk {
    datastore_id = var.disk_storage
    interface    = "scsi0"
    size         = var.disk_gb
  }

  initialization {
    datastore_id = var.disk_storage
    ip_config {
      ipv4 {
        address = "dhcp"
      }
    }
    user_account {
      username = "root"
      keys     = var.ssh_keys
    }
  }

  network_device {
    bridge = var.bridge
  }
}

output "vms" {
  value = { for k, v in proxmox_virtual_environment_vm.vm : k => v.ipv4_addresses }
}
`,
		},
	},
	{
		ID:      "lxc",
		Name:    "Debian container",
		Summary: "Downloads the Debian 13 LXC template and creates an unprivileged container with nesting, DHCP and your SSH keys.",
		Files: map[string]string{
			"main.tf": stackProvider + "\n" + stackClusterVars + `
variable "template_storage" {
  description = "A storage with the Container templates content type."
  type        = string
  default     = "__VZTMPL_STORAGE__"
}

variable "template_url" {
  description = "Current names are listed at http://download.proxmox.com/images/system/ (or run pveam available)."
  type        = string
  default     = "http://download.proxmox.com/images/system/debian-13-standard_13.1-2_amd64.tar.zst"
}

variable "hostname" {
  type    = string
  default = "ct-01"
}

variable "cores" {
  type    = number
  default = 1
}

variable "memory_mb" {
  type    = number
  default = 1024
}

variable "disk_gb" {
  type    = number
  default = 8
}

resource "proxmox_download_file" "template" {
  content_type = "vztmpl"
  datastore_id = var.template_storage
  node_name    = var.node
  url          = var.template_url
  overwrite    = false
}

resource "proxmox_virtual_environment_container" "ct" {
  node_name    = var.node
  unprivileged = true
  tags         = ["tofu"]

  features {
    nesting = true
  }

  cpu {
    cores = var.cores
  }

  memory {
    dedicated = var.memory_mb
  }

  disk {
    datastore_id = var.disk_storage
    size         = var.disk_gb
  }

  initialization {
    hostname = var.hostname
    ip_config {
      ipv4 {
        address = "dhcp"
      }
    }
    user_account {
      keys = var.ssh_keys
    }
  }

  network_interface {
    name   = "eth0"
    bridge = var.bridge
  }

  operating_system {
    template_file_id = proxmox_download_file.template.id
    type             = "debian"
  }
}
`,
		},
	},
	{
		ID:      "blank",
		Name:    "Empty Proxmox workspace",
		Summary: "The provider and the cluster's defaults only; write your own resources.",
		Files: map[string]string{
			"main.tf": stackProvider + "\n" + stackClusterVars,
		},
	},
}

// handleTofuStacks lists the stacks with defaults for one cluster.
func (a *API) handleTofuStacks(w http.ResponseWriter, r *http.Request) {
	s, err := a.pveScope(r)
	if err != nil {
		scopeErr(w, err)
		return
	}
	if s.TC != nil {
		writeErr(w, http.StatusNotFound, errNoCluster.Error())
		return
	}
	repl := map[string]string{
		"__NODE__": "pve", "__DISK_STORAGE__": "local-lvm", "__BRIDGE__": "vmbr0", "__IMAGE_STORAGE__": "local",
		"__VZTMPL_STORAGE__": "local", "__TEMPLATE_VMID__": "9000", "__SSH_KEY__": "",
	}
	if res, err := s.Cl.Resources(r.Context()); err == nil {
		var nodes []string
		for _, x := range res {
			if x.Type == "node" && x.Status == "online" {
				nodes = append(nodes, x.Node)
			}
		}
		slices.Sort(nodes)
		if len(nodes) > 0 {
			node := nodes[0]
			repl["__NODE__"] = node
			if t, err := templateTargets(r.Context(), s.Cl, node); err == nil {
				pick := func(k string, l ...[]string) {
					for _, x := range l {
						if len(x) > 0 {
							repl[k] = x[0]
							return
						}
					}
				}
				pick("__DISK_STORAGE__", t.DiskStorages)
				pick("__BRIDGE__", t.Bridges)
				pick("__IMAGE_STORAGE__", t.ImageStorages, t.FileStorages)
			}
			if tpl := newestTemplate(res, node); tpl != 0 {
				repl["__TEMPLATE_VMID__"] = strconv.Itoa(tpl)
			}
			var sts []pveStorageInfo
			if err := s.Cl.Do(r.Context(), http.MethodGet, nodePath(node)+"/storage", nil, &sts); err == nil {
				for _, st := range sts {
					if st.Active != 0 && st.has("vztmpl") {
						repl["__VZTMPL_STORAGE__"] = st.Storage
						break
					}
				}
			}
		}
	}
	if _, pub, err := a.ansibleKey(); err == nil {
		repl["__SSH_KEY__"] = strings.TrimSpace(pub) + " servermgr-ansible"
	}
	out := make([]tofuStack, 0, len(tofuStacks))
	for _, st := range tofuStacks {
		files := map[string]string{}
		for name, body := range st.Files {
			for k, v := range repl {
				body = strings.ReplaceAll(body, k, v)
			}
			// No key: an empty list rather than [""].
			files[name] = strings.ReplaceAll(body, `default     = [""]`, "default     = []")
		}
		st.Files = files
		out = append(out, st)
	}
	writeJSON(w, http.StatusOK, out)
}

// newestTemplate is the newest QEMU template on the node.
func newestTemplate(res []proxmox.Resource, node string) int {
	best := 0
	for _, x := range res {
		if x.Type == "qemu" && x.Template == 1 && x.Node == node && x.VMID > best {
			best = x.VMID
		}
	}
	return best
}
