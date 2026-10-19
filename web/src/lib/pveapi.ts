import { ApiError, request } from "@/lib/api";

// Proxmox management beyond the guest list: guest hardware and lifecycle,
// nodes, storage and datacenter settings. Parameters go to Proxmox as they
// are, so their names are Proxmox's own.

export type PVERun = { upid: string; done: boolean };
export type PVENodeStorage = {
  storage: string;
  type: string;
  content: string;
  active: number;
  enabled: number;
  shared: number;
  avail: number;
  total: number;
  used: number;
};
export type PVEChoices = {
  storages: PVENodeStorage[];
  isos: string[];
  templates: string[];
  bridges: string[];
  nodes: string[];
  nextid?: number;
};
export type PVEAppliance = {
  template: string;
  package: string;
  section: string;
  headline: string;
  description?: string;
  version?: string;
  os?: string;
  infopage?: string;
};
export type PVETaskRow = { upid: string; type: string; user: string; status?: string; starttime: number; endtime?: number; id?: string; node?: string };
export type PVEVolumeRow = { volid: string; format?: string; size: number; ctime?: number; content: string; vmid?: number | string; notes?: string };
export type WinEdition = { id: string; name: string; ostype: string; virtio: string; index: number; client: boolean };
export type WinProfile = "standard" | "hardened";
export type WinBuildReq = {
  edition: string;
  node: string;
  iso: string;
  drivers_iso: string;
  disk_storage: string;
  bridge: string;
  vlan?: number;
  cores: number;
  memory_mb: number;
  disk_gb: number;
  image_index: number;
  product_key: string;
  locale: string;
  keyboard: string;
  timezone: string;
  profiles: WinProfile[];
  name_prefix: string;
};
export type Params = Record<string, string | number | boolean>;
export type Row = Record<string, unknown>;

const np = (id: string, node: string) => `/api/v1/proxmox/${id}/nodes/${encodeURIComponent(node)}`;
const dp = (id: string, coll: string) => `/api/v1/proxmox/${id}/dc/${coll}`;
const enc = encodeURIComponent;

export const pve = {
  // creating guests
  create: (id: string, node: string, type: "qemu" | "lxc", params: Record<string, string>) =>
    request<{ upid: string; vmid: number; node: string; type: string }>("POST", `${np(id, node)}/guests`, { type, params }),
  choices: (id: string, node: string) => request<PVEChoices>("GET", `${np(id, node)}/choices`),
  appliances: (id: string, node: string) => request<PVEAppliance[]>("GET", `${np(id, node)}/appliances`),
  applianceDownload: (id: string, node: string, storage: string, template: string) =>
    request<PVERun>("POST", `${np(id, node)}/appliances`, { storage, template }),

  // Windows templates (Cloudbase-Init)
  // windowsISO has the node download the retail Windows 10/11 ISO in a
  // language straight from Microsoft.
  windowsISO: (id: string, node: string, body: { edition: string; locale: string; storage: string }) =>
    request<{ volid: string; file: string; index: number; upid?: string }>("POST", `/api/v1/proxmox/${id}/nodes/${enc(node)}/windows-iso`, body),
  windowsAdmin: (id: string, node: string, vmid: number, password: string) =>
    request<void>("POST", `/api/v1/proxmox/${id}/guests/${enc(node)}/qemu/${vmid}/windows-admin`, { password }),
  windowsBuild: (id: string, req: WinBuildReq) => request<{ id: string; name: string }[]>("POST", `/api/v1/proxmox/${id}/windows-templates`, req),
  windowsPrep: (id: string, node: string, vmid: number, storage: string) =>
    request<{ volid: string; slot: string; pending: boolean }>("POST", `/api/v1/proxmox/${id}/guests/${enc(node)}/qemu/${vmid}/windows-prep`, { storage }),
  windowsTemplate: (id: string, node: string, vmid: number, confirm: string, profile: WinProfile | "") =>
    request<{ log: string[] }>("POST", `/api/v1/proxmox/${id}/guests/${enc(node)}/qemu/${vmid}/windows-template`, { confirm, profile }),

  // nodes
  nodeView: <T = unknown>(id: string, node: string, what: string, query = "") => request<T>("GET", `${np(id, node)}/view/${what}${query}`),
  nodeSet: (id: string, node: string, what: string, params: Params) => request<PVERun>("PUT", `${np(id, node)}/view/${what}`, { params }),
  syslog: (id: string, node: string, limit = 500, service = "") =>
    request<{ n: number; t: string }[]>("GET", `${np(id, node)}/syslog?limit=${limit}${service ? `&service=${enc(service)}` : ""}`),
  nodeTasks: (id: string, node: string, errors = false) => request<PVETaskRow[]>("GET", `${np(id, node)}/tasks${errors ? "?errors=1" : ""}`),
  nodeTaskLog: (id: string, node: string, upid: string) =>
    request<{ log: string; status: { status?: string; exitstatus?: string } | null }>("GET", `${np(id, node)}/tasks/log?upid=${enc(upid)}`),
  nodeTaskStop: (id: string, node: string, upid: string) => request<PVERun>("DELETE", `${np(id, node)}/tasks?upid=${enc(upid)}`),
  nodePower: (id: string, node: string, command: "reboot" | "shutdown", confirm: string) =>
    request<PVERun>("POST", `${np(id, node)}/power`, { command, confirm }),
  service: (id: string, node: string, svc: string, action: string) => request<PVERun>("POST", `${np(id, node)}/services/${enc(svc)}/${action}`),
  aptRefresh: (id: string, node: string) => request<PVERun>("POST", `${np(id, node)}/apt/refresh`),
  bulk: (id: string, node: string, action: "startall" | "stopall" | "migrateall", target = "") =>
    request<PVERun>("POST", `${np(id, node)}/bulk`, { action, target }),
  netCreate: (id: string, node: string, params: Params) => request<PVERun>("POST", `${np(id, node)}/network`, { params }),
  netUpdate: (id: string, node: string, iface: string, params: Params) => request<PVERun>("PUT", `${np(id, node)}/network/${enc(iface)}`, { params }),
  netDelete: (id: string, node: string, iface: string) => request<PVERun>("DELETE", `${np(id, node)}/network/${enc(iface)}`),
  netApply: (id: string, node: string) => request<PVERun>("PUT", `${np(id, node)}/network`),
  netRevert: (id: string, node: string) => request<PVERun>("DELETE", `${np(id, node)}/network`),
  disk: (id: string, node: string, action: string, params: Params, confirm: string) =>
    request<PVERun>("POST", `${np(id, node)}/disks/${action}`, { params, confirm }),
  shell: (id: string, node: string, cmd: "" | "upgrade") => request<{ sid: string; mode: "term" }>("POST", `${np(id, node)}/shell`, { cmd }),

  // storage
  upload: (id: string, node: string, storage: string, content: string, file: File, onProgress: (p: number) => void) =>
    new Promise<PVERun>((resolve, reject) => {
      const x = new XMLHttpRequest();
      x.open("POST", `${np(id, node)}/storage/${enc(storage)}/upload?content=${content}&filename=${enc(file.name)}`);
      x.setRequestHeader("X-Requested-With", "servermgr");
      x.setRequestHeader("Content-Type", "application/octet-stream");
      x.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
      x.onload = () => {
        let data: { error?: string } & Partial<PVERun> = {};
        try {
          data = JSON.parse(x.responseText);
        } catch {
          /* empty */
        }
        if (x.status >= 200 && x.status < 300) resolve(data as PVERun);
        else reject(new ApiError(x.status, data.error ?? x.statusText, data));
      };
      x.onerror = () => reject(new Error("upload failed"));
      x.send(file);
    }),

  // datacenter
  dcList: <T = Row[]>(id: string, coll: string) => request<T>("GET", dp(id, coll)),
  dcGet: <T = Row>(id: string, coll: string, key: string) => request<T>("GET", `${dp(id, coll)}/${enc(key)}`),
  dcCreate: (id: string, coll: string, params: Params) => request<PVERun>("POST", dp(id, coll), { params }),
  dcUpdate: (id: string, coll: string, key: string | null, params: Params) =>
    request<PVERun>("PUT", key === null ? dp(id, coll) : `${dp(id, coll)}/${enc(key)}`, { params }),
  dcDelete: (id: string, coll: string, key: string) => request<PVERun>("DELETE", `${dp(id, coll)}/${enc(key)}`),
  dcItems: (id: string, coll: string, key: string) => request<Row[]>("GET", `${dp(id, coll)}/${enc(key)}/items`),
  dcItemAdd: (id: string, coll: string, key: string, params: Params) => request<PVERun>("POST", `${dp(id, coll)}/${enc(key)}/items`, { params }),
  dcItemDelete: (id: string, coll: string, key: string, item: string) => request<PVERun>("DELETE", `${dp(id, coll)}/${enc(key)}/items/${enc(item)}`),
  sdnApply: (id: string) => request<PVERun>("POST", `/api/v1/proxmox/${id}/sdn/apply`),
};

// ranMsg describes the result of a call that may have started a task.
export function ranMsg(r: PVERun | undefined, done: string, started = "Started; follow it under Tasks.") {
  return r && !r.done && r.upid ? started : done;
}
