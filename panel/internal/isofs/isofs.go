// Package isofs writes small ISO 9660 images with Joliet names: one root
// directory of a few files, enough for answer files and scripts that a VM
// reads from a virtual CD.
package isofs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
)

const sector = 2048

// File is one file in the image's root directory. Name is the long
// (Joliet) name; Short is the 8.3 ISO 9660 name, upper case.
type File struct {
	Name  string
	Short string
	Data  []byte
}

// Build returns an ISO image holding files, labelled label.
func Build(label string, files []File, now time.Time) ([]byte, error) {
	if len(label) == 0 || len(label) > 16 {
		return nil, errors.New("label: 1 to 16 characters")
	}
	files = slices.Clone(files)
	seen := map[string]bool{}
	for _, f := range files {
		if !validShort(f.Short) || len(f.Name) == 0 || len(f.Name) > 64 || seen[f.Short] || seen[strings.ToLower(f.Name)] {
			return nil, fmt.Errorf("bad or duplicate file name %q (%q)", f.Name, f.Short)
		}
		seen[f.Short], seen[strings.ToLower(f.Name)] = true, true
	}
	// Layout: 16 system sectors, PVD, Joliet SVD, terminator, four path
	// tables, two root directories, then the files.
	const (
		pvdAt, svdAt, termAt = 16, 17, 18
		lPath, mPath         = 19, 20
		jlPath, jmPath       = 21, 22
		rootAt, jrootAt      = 23, 24
		firstFile            = 25
	)
	at := make([]uint32, len(files))
	next := uint32(firstFile)
	for i, f := range files {
		at[i] = next
		next += uint32((len(f.Data) + sector - 1) / sector)
		if len(f.Data) == 0 {
			at[i] = 0
		}
	}
	total := next
	img := make([]byte, int(total)*sector)
	put := func(lba uint32, b []byte) { copy(img[int(lba)*sector:], b) }

	short := slices.Clone(files)
	slices.SortFunc(short, func(a, b File) int { return strings.Compare(a.Short, b.Short) })
	long := slices.Clone(files)
	slices.SortFunc(long, func(a, b File) int { return slices.Compare(ucs2(a.Name), ucs2(b.Name)) })
	where := map[string]uint32{}
	for i, f := range files {
		where[f.Short] = at[i]
	}

	dir := func(self uint32, list []File, joliet bool) []byte {
		var b bytes.Buffer
		b.Write(record(self, sector, 2, []byte{0}, now))
		b.Write(record(self, sector, 2, []byte{1}, now))
		for _, f := range list {
			name := []byte(f.Short + ";1")
			if joliet {
				name = ucs2(f.Name)
			}
			b.Write(record(where[f.Short], uint32(len(f.Data)), 0, name, now))
		}
		return b.Bytes()
	}
	rd, jrd := dir(rootAt, short, false), dir(jrootAt, long, true)
	if len(rd) > sector || len(jrd) > sector {
		return nil, errors.New("too many files")
	}
	put(rootAt, rd)
	put(jrootAt, jrd)
	pathTable := func(root uint32, big bool) []byte {
		b := make([]byte, 10)
		b[0] = 1
		if big {
			binary.BigEndian.PutUint32(b[2:], root)
			binary.BigEndian.PutUint16(b[6:], 1)
		} else {
			binary.LittleEndian.PutUint32(b[2:], root)
			binary.LittleEndian.PutUint16(b[6:], 1)
		}
		return b
	}
	put(lPath, pathTable(rootAt, false))
	put(mPath, pathTable(rootAt, true))
	put(jlPath, pathTable(jrootAt, false))
	put(jmPath, pathTable(jrootAt, true))
	put(pvdAt, descriptor(1, label, total, lPath, mPath, rootAt, now))
	put(svdAt, descriptor(2, label, total, jlPath, jmPath, jrootAt, now))
	put(termAt, append([]byte{255}, "CD001\x01"...))
	for i, f := range files {
		if at[i] != 0 {
			put(at[i], f.Data)
		}
	}
	return img, nil
}

func validShort(s string) bool {
	base, ext, _ := strings.Cut(s, ".")
	if len(base) == 0 || len(base) > 8 || len(ext) > 3 || strings.Contains(ext, ".") {
		return false
	}
	for _, r := range base + ext {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func ucs2(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.BigEndian.AppendUint16(b, u)
	}
	return b
}

func both32(b []byte, v uint32) {
	binary.LittleEndian.PutUint32(b, v)
	binary.BigEndian.PutUint32(b[4:], v)
}

func both16(b []byte, v uint16) {
	binary.LittleEndian.PutUint16(b, v)
	binary.BigEndian.PutUint16(b[2:], v)
}

// record is a directory record.
func record(extent, size uint32, flags byte, name []byte, t time.Time) []byte {
	n := 33 + len(name)
	if n%2 == 1 {
		n++
	}
	b := make([]byte, n)
	b[0] = byte(n)
	both32(b[2:], extent)
	both32(b[10:], size)
	t = t.UTC()
	copy(b[18:], []byte{byte(t.Year() - 1900), byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second()), 0})
	b[25] = flags
	both16(b[28:], 1)
	b[32] = byte(len(name))
	copy(b[33:], name)
	return b
}

// descriptor is the primary (kind 1) or Joliet supplementary (kind 2)
// volume descriptor.
func descriptor(kind byte, label string, total, lPath, mPath, root uint32, t time.Time) []byte {
	b := make([]byte, sector)
	b[0] = kind
	copy(b[1:], "CD001")
	b[6] = 1
	text := func(off, n int, s string) {
		if kind == 2 {
			u := ucs2(s)
			for i := 0; i < n; i += 2 {
				if i+1 < len(u) {
					b[off+i], b[off+i+1] = u[i], u[i+1]
				} else {
					b[off+i], b[off+i+1] = 0, ' '
				}
			}
			return
		}
		copy(b[off:off+n], fmt.Sprintf("%-*s", n, strings.ToUpper(s)))
	}
	text(8, 32, "")
	text(40, 32, label)
	both32(b[80:], total)
	if kind == 2 {
		copy(b[88:], "%/E") // UCS-2 level 3
	}
	both16(b[120:], 1)
	both16(b[124:], 1)
	both16(b[128:], sector)
	both32(b[132:], 10)
	binary.LittleEndian.PutUint32(b[140:], lPath)
	binary.BigEndian.PutUint32(b[148:], mPath)
	copy(b[156:], record(root, sector, 2, []byte{0}, t))
	for _, f := range [][2]int{{190, 128}, {318, 128}, {446, 128}, {574, 128}, {702, 37}, {739, 37}, {776, 37}} {
		text(f[0], f[1], "")
	}
	text(574, 128, "SERVERMGR")
	stamp := []byte(t.UTC().Format("2006010215040500") + "\x00")
	copy(b[813:], stamp)
	copy(b[830:], stamp)
	copy(b[847:], "0000000000000000\x00")
	copy(b[864:], "0000000000000000\x00")
	b[881] = 1
	return b
}
