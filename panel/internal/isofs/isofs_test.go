package isofs

import (
	"bytes"
	"testing"
	"time"
)

func TestBuild(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 5000)
	img, err := Build("SMWIN", []File{
		{Name: "autounattend.xml", Short: "AUTOUNAT.XML", Data: []byte("<unattend/>")},
		{Name: "servermgr-prepare.ps1", Short: "SMPREP.PS1", Data: big},
	}, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(img)%sector != 0 || len(img) != (25+1+3)*sector {
		t.Errorf("size %d", len(img))
	}
	if string(img[16*sector+1:16*sector+6]) != "CD001" || img[17*sector] != 2 || img[18*sector] != 255 {
		t.Error("descriptors")
	}
	if !bytes.Contains(img[24*sector:25*sector], ucs2("autounattend.xml")) || !bytes.Contains(img[23*sector:24*sector], []byte("SMPREP.PS1;1")) {
		t.Error("directories")
	}
	if !bytes.Equal(img[26*sector:26*sector+5000], big) {
		t.Error("file data")
	}
	if _, err := Build("X", []File{{Name: "a", Short: "toolongname.txt"}}, time.Now()); err == nil {
		t.Error("bad short name accepted")
	}
}
