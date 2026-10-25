package recent

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/tsdlanm/cli_app/internal/sizer"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setCreated mengubah waktu dibuat file, seperti file yang sudah lama ada.
func setCreated(t *testing.T, path string, ct time.Time) {
	t.Helper()
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	ft := windows.NsecToFiletime(ct.UnixNano())
	if err := windows.SetFileTime(h, &ft, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestScanFiles(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	opt := Options{Days: 7, MinFile: 1000}
	files := []struct {
		rel     string
		size    int
		created time.Time
	}{
		{`Downloads\ubuntu.iso`, 5000, now.Add(-2 * time.Hour)}, // pengguna
		{`Videos\rekaman.mp4`, 3000, now.Add(-1 * time.Hour)},   // pengguna
		{`Documents\lama.pdf`, 4000, now.AddDate(-1, 0, 0)},     // lama: tidak dihitung
		{`Desktop\catatan.txt`, 10, now},                        // baru tapi kecil: hanya dihitung
		{`AppData\Local\app\data.bin`, 2000, now},               // aplikasi
		{`proj\node_modules\pkg\index.js`, 2000, now},           // cache
		{`AppData\Local\Chrome\Cache\thumb.db`, 2000, now},      // cache (lebih spesifik dari aplikasi)
		{`Windows\System32\driver.sys`, 2000, now},              // sistem (root drive)
	}
	for _, f := range files {
		p := filepath.Join(root, f.rel)
		write(t, p, f.size)
		setCreated(t, p, f.created)
	}

	rep, err := Scan(t.Context(), []Root{{Path: root}}, opt, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := rep.Drives[0]
	// Semua file baru yang cukup besar didaftar, termasuk buatan sistem dan
	// aplikasi, urut dari yang paling baru; file lama tidak ikut.
	kinds := map[string]Kind{}
	var order []string
	for _, f := range d.Files {
		kinds[filepath.Base(f.Path)] = f.Kind
		order = append(order, filepath.Base(f.Path))
	}
	if len(order) != 6 || order[4] != "rekaman.mp4" || order[5] != "ubuntu.iso" {
		t.Errorf("Files = %v, want 4 file terbaru lalu rekaman.mp4, ubuntu.iso", order)
	}
	want := map[string]Kind{
		"ubuntu.iso": KindUser, "rekaman.mp4": KindUser, "data.bin": KindApp,
		"index.js": KindCache, "thumb.db": KindCache, "driver.sys": KindSystem,
	}
	for name, k := range want {
		if kinds[name] != k {
			t.Errorf("label %s = %q, want %q", name, kinds[name], k)
		}
	}
	if d.FileSize != 5000+3000+2000*4 {
		t.Errorf("FileSize = %d", d.FileSize)
	}
	// File kecil tetap terhitung, tidak hilang.
	if d.SmallCount != 1 || d.SmallSize != 10 {
		t.Errorf("Small = %d file, %d byte; want 1 file, 10 byte", d.SmallCount, d.SmallSize)
	}
}

// Kasus nyata: file hasil ekstrak zip bertanggal dibuat 1601 (FILETIME kosong)
// dulu terbaca sebagai "1 menit lalu" karena overflow.
func TestScanInvalidCreation(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "replit_zip_error_log.txt")
	write(t, p, 5000)
	setCreated(t, p, time.Date(1601, 10, 4, 16, 49, 27, 0, time.UTC))
	old := time.Date(2023, 5, 20, 17, 41, 13, 0, time.Local)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	// Tanggal dibuat di masa depan juga tidak dipercaya.
	q := filepath.Join(root, "masa_depan.bin")
	write(t, q, 5000)
	setCreated(t, q, time.Now().AddDate(5, 0, 0))
	if err := os.Chtimes(q, old, old); err != nil {
		t.Fatal(err)
	}

	rep, err := Scan(t.Context(), []Root{{Path: root}}, Options{Days: 7, MinFile: 1000}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(rep.Drives[0].Files); n != 0 {
		t.Errorf("file bertanggal dibuat tidak valid dianggap baru: %+v", rep.Drives[0].Files)
	}
}

func TestScanOnDir(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "x.bin"), 3000)
	write(t, filepath.Join(root, "a", "b", "y.bin"), 2000)

	got := map[string]sizer.Info{}
	var mu sync.Mutex
	opt := DefaultOptions
	opt.OnDir = func(path string, inf sizer.Info) {
		mu.Lock()
		got[path] = inf
		mu.Unlock()
	}
	if _, err := Scan(t.Context(), []Root{{Path: root}}, opt, "", nil); err != nil {
		t.Fatal(err)
	}
	// Setiap folder dilaporkan dengan ukuran dan jumlah rekursif.
	if inf := got[filepath.Join(root, "a")]; inf.Size != 5000 || inf.Files != 2 || inf.Dirs != 1 {
		t.Errorf("a = %+v, want 5000 byte, 2 file, 1 folder", inf)
	}
	if inf := got[filepath.Join(root, "a", "b")]; inf.Size != 2000 || inf.Files != 1 {
		t.Errorf("a\\b = %+v", inf)
	}
	if inf := got[root]; inf.Size != 5000 || inf.Dirs != 2 {
		t.Errorf("root = %+v", inf)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	path := SnapshotPath(t.TempDir())
	now := time.Now().Truncate(time.Second)
	in := &Snapshot{
		Time: now,
		Report: &Report{Since: now.AddDate(0, 0, -7), Drives: []DriveReport{{
			Root: `C:\`, FileSize: 5000,
			Files: []File{{Path: `C:\dl\a.iso`, Size: 5000, Created: now, Kind: KindUser}},
		}}},
		Dirs: map[string]sizer.Info{`C:\dl`: {Size: 5000, Files: 1}},
	}
	if err := SaveSnapshot(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Time.Equal(now) || out.Report.Drives[0].Files[0].Path != `C:\dl\a.iso` || out.Dirs[`C:\dl`].Size != 5000 {
		t.Errorf("snapshot = %+v", out)
	}
	if _, err := LoadSnapshot(path + ".tidak-ada"); err == nil {
		t.Error("snapshot yang tidak ada seharusnya error")
	}
}

func TestScanGrowth(t *testing.T) {
	root, base := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "a.bin"), 10000)

	rep, err := Scan(t.Context(), []Root{{Path: root}}, DefaultOptions, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Drives[0].GrowthSince.IsZero() {
		t.Fatal("pemindaian pertama seharusnya belum punya pembanding")
	}

	write(t, filepath.Join(root, "b.bin"), 5000)
	rep, err = Scan(t.Context(), []Root{{Path: root}}, DefaultOptions, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := rep.Drives[0]; d.GrowthSince.IsZero() || d.Growth != 5000 {
		t.Errorf("Growth = %d sejak %v, want +5000", d.Growth, d.GrowthSince)
	}
}
