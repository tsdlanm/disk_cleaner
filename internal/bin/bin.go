// Package bin membaca isi Recycle Bin Windows milik pengguna saat ini, dan
// memulihkan atau menghapus permanen itemnya.
//
// Setiap item terhapus disimpan di X:\$Recycle.Bin\<SID>\ sebagai dua file:
// $I<kode> berisi lokasi asal, ukuran, dan waktu hapus; $R<kode> berisi file
// atau folder itu sendiri.
package bin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Item adalah satu item di Recycle Bin.
type Item struct {
	Original string    // lokasi asal sebelum dihapus
	Data     string    // file/folder $R di Recycle Bin
	Info     string    // file $I
	Size     int64     // ukuran (untuk folder: seluruh isinya)
	Deleted  time.Time // waktu dihapus
	IsDir    bool
}

// Name adalah nama item saat masih di lokasi asalnya.
func (it Item) Name() string { return filepath.Base(it.Original) }

// List membaca isi Recycle Bin pengguna saat ini di setiap root (misalnya
// "C:\"), dari yang terbesar. Drive yang tidak punya Recycle Bin dilewati.
func List(roots []string) ([]Item, error) {
	sid, err := userSID()
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, root := range roots {
		dir := filepath.Join(root, "$Recycle.Bin", sid)
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if len(name) < 3 || !strings.EqualFold(name[:2], "$I") {
				continue
			}
			it, err := readInfo(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			it.Data = filepath.Join(dir, "$R"+name[2:])
			fi, err := os.Lstat(it.Data)
			if err != nil {
				continue // isinya sudah tidak ada
			}
			it.IsDir = fi.IsDir()
			out = append(out, it)
		}
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		if a.Size != b.Size {
			if a.Size > b.Size {
				return -1
			}
			return 1
		}
		return b.Deleted.Compare(a.Deleted)
	})
	return out, nil
}

// readInfo membaca file $I: versi (8 byte), ukuran (8), waktu hapus
// (FILETIME, 8), lalu lokasi asal. Versi 1 (Windows Vista–8.1) menyimpan
// lokasi sebagai 260 karakter UTF-16; versi 2 (Windows 10+) didahului
// panjangnya (4 byte, dalam karakter).
func readInfo(path string) (Item, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Item{}, err
	}
	if len(b) < 24 {
		return Item{}, errors.New("file $I terlalu pendek")
	}
	le := binary.LittleEndian
	it := Item{Info: path, Size: int64(le.Uint64(b[8:]))}
	ft := windows.Filetime{LowDateTime: le.Uint32(b[16:]), HighDateTime: le.Uint32(b[20:])}
	it.Deleted = time.Unix(0, ft.Nanoseconds())

	var raw []byte
	switch le.Uint64(b) {
	case 1:
		raw = b[24:min(len(b), 24+520)]
	case 2:
		if len(b) < 28 {
			return Item{}, errors.New("file $I terlalu pendek")
		}
		n := int(le.Uint32(b[24:]))
		raw = b[28:min(len(b), 28+2*n)]
	default:
		return Item{}, fmt.Errorf("versi $I %d tidak dikenal", le.Uint64(b))
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = le.Uint16(raw[2*i:])
	}
	if i := slices.Index(u, 0); i >= 0 {
		u = u[:i]
	}
	it.Original = string(utf16.Decode(u))
	if it.Original == "" {
		return Item{}, errors.New("lokasi asal kosong")
	}
	return it, nil
}

// Restore mengembalikan item ke lokasi asalnya. Gagal jika di sana sudah
// ada file/folder dengan nama yang sama.
func Restore(it Item) error {
	if _, err := os.Lstat(it.Original); err == nil {
		return fmt.Errorf("%s sudah ada", it.Original)
	}
	if err := os.MkdirAll(filepath.Dir(it.Original), 0o755); err != nil {
		return err
	}
	if err := os.Rename(it.Data, it.Original); err != nil {
		return err
	}
	return os.Remove(it.Info)
}

// Purge menghapus item secara permanen.
func Purge(it Item) error {
	if err := os.RemoveAll(it.Data); err != nil {
		return err
	}
	return os.Remove(it.Info)
}

var procEmpty = windows.NewLazySystemDLL("shell32.dll").NewProc("SHEmptyRecycleBinW")

// Empty mengosongkan Recycle Bin di semua drive, tanpa dialog Windows
// (konfirmasi sudah dilakukan di TUI).
func Empty() error {
	const noConfirmation, noProgressUI, noSound = 0x1, 0x2, 0x4
	r, _, _ := procEmpty.Call(0, 0, noConfirmation|noProgressUI|noSound)
	// S_OK, atau E_UNEXPECTED jika Recycle Bin memang sudah kosong.
	if r != 0 && uint32(r) != 0x8000FFFF {
		return fmt.Errorf("Windows menolak mengosongkan Recycle Bin (kode %#x)", uint32(r))
	}
	return nil
}

// userSID adalah SID pengguna saat ini, nama folder Recycle Bin miliknya.
func userSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String(), nil
}
