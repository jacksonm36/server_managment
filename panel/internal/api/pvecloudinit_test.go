package api

import "testing"

func TestHasCloudInitDrive(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]any
		want bool
	}{
		{map[string]any{"ide2": "local:iso/debian.iso,media=cdrom", "scsi0": "x:112/base-112-disk-2.qcow2"}, false},
		{map[string]any{"ide2": "local-lvm:vm-8001-cloudinit,media=cdrom", "scsi0": "x:disk"}, true},
		{map[string]any{"description": "cloudinit", "scsi0": "x:disk"}, false},
	} {
		if got := hasCloudInitDrive(c.cfg); got != c.want {
			t.Errorf("%v: got %v", c.cfg, got)
		}
	}
}
