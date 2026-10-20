// Package drives membaca daftar drive Windows beserta kapasitasnya.
package drives

import (
	"golang.org/x/sys/windows"
)

// Drive adalah satu drive Windows, misalnya C:\.
type Drive struct {
	Root  string // "C:\"
	Label string // nama volume, misalnya "Windows"
	Kind  string // "Lokal", "USB", "Jaringan", ...
	Total uint64
	Free  uint64
	Ready bool // false untuk drive tanpa media (DVD kosong, card reader)
}

// Used mengembalikan jumlah byte yang terpakai.
func (d Drive) Used() uint64 {
	if d.Free > d.Total {
		return 0
	}
	return d.Total - d.Free
}

func init() {
	// Cegah dialog "No disk" Windows saat membaca drive tanpa media.
	windows.SetErrorMode(windows.SEM_FAILCRITICALERRORS)
}

// List mengembalikan semua drive yang terpasang, urut menurut huruf.
func List() []Drive {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return nil
	}
	var out []Drive
	for i := range 26 {
		if mask&(1<<i) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		p, _ := windows.UTF16PtrFromString(root)
		d := Drive{Root: root, Kind: kind(windows.GetDriveType(p))}

		var avail, total, free uint64
		if windows.GetDiskFreeSpaceEx(p, &avail, &total, &free) == nil {
			d.Ready = true
			d.Total = total
			d.Free = free
			var name [windows.MAX_PATH + 1]uint16
			if windows.GetVolumeInformation(p, &name[0], uint32(len(name)), nil, nil, nil, nil, 0) == nil {
				d.Label = windows.UTF16ToString(name[:])
			}
		}
		out = append(out, d)
	}
	return out
}

func kind(t uint32) string {
	switch t {
	case windows.DRIVE_FIXED:
		return "Lokal"
	case windows.DRIVE_REMOVABLE:
		return "USB"
	case windows.DRIVE_REMOTE:
		return "Jaringan"
	case windows.DRIVE_CDROM:
		return "CD/DVD"
	case windows.DRIVE_RAMDISK:
		return "RAM disk"
	}
	return ""
}
