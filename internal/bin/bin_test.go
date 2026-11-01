package bin

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// writeInfo menulis file $I versi ver untuk original.
func writeInfo(t *testing.T, path string, ver int, original string, size int64, at time.Time) {
	t.Helper()
	le := binary.LittleEndian
	b := make([]byte, 24)
	le.PutUint64(b, uint64(ver))
	le.PutUint64(b[8:], uint64(size))
	ft := windows.NsecToFiletime(at.UnixNano())
	le.PutUint32(b[16:], ft.LowDateTime)
	le.PutUint32(b[20:], ft.HighDateTime)
	name := append(utf16.Encode([]rune(original)), 0)
	if ver == 1 {
		name = append(name, make([]uint16, 260-len(name))...)
	} else {
		b = le.AppendUint32(b, uint32(len(name)))
	}
	for _, c := range name {
		b = le.AppendUint16(b, c)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadInfo(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 30, 14, 5, 0, 0, time.UTC)
	for _, ver := range []int{1, 2} {
		p := filepath.Join(dir, "$IABC123.iso")
		writeInfo(t, p, ver, `D:\unduhan\Ubuntu 24.04.iso`, 5<<30, at)
		it, err := readInfo(p)
		if err != nil {
			t.Fatalf("versi %d: %v", ver, err)
		}
		if it.Original != `D:\unduhan\Ubuntu 24.04.iso` || it.Size != 5<<30 || !it.Deleted.Equal(at) || it.Name() != "Ubuntu 24.04.iso" {
			t.Errorf("versi %d: %+v", ver, it)
		}
	}
	os.WriteFile(filepath.Join(dir, "rusak"), []byte{1, 2, 3}, 0o644)
	if _, err := readInfo(filepath.Join(dir, "rusak")); err == nil {
		t.Error("file $I rusak terbaca")
	}
}

func TestRestorePurge(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "asal", "foto.jpg")
	it := Item{Original: orig, Data: filepath.Join(dir, "$RX.jpg"), Info: filepath.Join(dir, "$IX.jpg")}
	os.WriteFile(it.Data, []byte("isi"), 0o644)
	writeInfo(t, it.Info, 2, orig, 3, time.Now())

	if err := Restore(it); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(orig); string(b) != "isi" {
		t.Errorf("isi setelah dipulihkan = %q", b)
	}
	if _, err := os.Stat(it.Info); err == nil {
		t.Error("$I masih ada setelah dipulihkan")
	}

	// Lokasi asal sudah terisi: tidak ditimpa.
	os.WriteFile(it.Data, []byte("lain"), 0o644)
	writeInfo(t, it.Info, 2, orig, 4, time.Now())
	if err := Restore(it); err == nil {
		t.Error("file yang sudah ada ditimpa")
	}
	if err := Purge(it); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{it.Data, it.Info} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s masih ada setelah dihapus permanen", p)
		}
	}
}
