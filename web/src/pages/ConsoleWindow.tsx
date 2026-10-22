import { useEffect } from "react";
import { GuestConsole } from "@/components/GuestConsole";

// ConsoleWindow is a guest console in its own browser window (the console's
// pop-out button). It is the whole page, so it covers the panel's shell.
export function ConsoleWindow({ cluster, node, type, vmid }: { cluster: string; node: string; type: string; vmid: string }) {
  const q = new URLSearchParams(location.search);
  const name = q.get("name") || `${type === "lxc" ? "CT" : "VM"} ${vmid}`;
  useEffect(() => {
    document.title = `${name} · console`;
  }, [name]);
  if (type !== "qemu" && type !== "lxc") return null;
  return (
    <GuestConsole
      cluster={cluster}
      guest={{ node, type, vmid: Number(vmid) }}
      name={name}
      sharedClip={q.get("clip") === "1"}
      popout
      onClose={() => window.close()}
    />
  );
}
