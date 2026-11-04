package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsdlanm/cli_app/internal/bin"
	"github.com/tsdlanm/cli_app/internal/drives"
	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/sizer"
	"github.com/tsdlanm/cli_app/internal/trend"
)

// tempDir membuat folder uji berejaan panjang, seperti drive yang dipindai
// dari C:\ (t.TempDir mengikuti TEMP yang bisa berejaan pendek 8.3).
func tempDir(t *testing.T) string { return recent.LongPath(t.TempDir()) }

func TestLiveUpdate(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := tempDir(t)
	vol := filepath.VolumeName(root) + `\`
	oldDir, newDir := filepath.Join(root, "lama"), filepath.Join(root, "unduhan")
	old := filepath.Join(oldDir, "a.bin")
	write(t, old, 2<<20)

	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{{
		Root: vol, Files: []recent.File{{Path: old, Size: 2 << 20, Created: time.Now().Add(-time.Hour)}},
	}}}, at: time.Now()})
	key(m, "tab") // halaman File baru: kursor di judul drive
	key(m, "down")
	sel := m.selectedRecent()
	if sel == "" {
		t.Fatal("kursor tidak di File baru")
	}
	run(m, loadDir(root))
	before := len(m.rows)

	// File baru di folder lain: masuk di atas, kursor tetap di file yang
	// sama, dan folder yang sedang dibuka ikut dimuat ulang.
	nf := filepath.Join(newDir, "x.iso")
	write(t, nf, 3<<20)
	opt := m.recentOptions()
	_, cmd := m.Update(liveMsg{changes: recent.Check([]string{nf, newDir}, opt, time.Now()), dirs: map[string]bool{root: true, newDir: true}})
	if f := m.recentFiles(); len(f) != 2 || f[0].Path != nf {
		t.Fatalf("file = %+v", f)
	}
	if m.selectedRecent() != sel {
		t.Errorf("kursor pindah dari %q ke %q", sel, m.selectedRecent())
	}
	if !m.liveDirty {
		t.Error("perubahan tidak ditandai untuk disimpan")
	}
	run(m, cmd)
	if len(m.rows) != before {
		// root berisi lama\ dan unduhan\ (baru): satu baris bertambah.
		if len(m.rows) != before+1 {
			t.Errorf("isi folder tidak dimuat ulang: %d baris, want %d", len(m.rows), before+1)
		}
	} else {
		t.Error("isi folder tidak dimuat ulang")
	}

	// Folder dihapus: hilang dari daftar.
	os.RemoveAll(oldDir)
	m.Update(liveMsg{changes: recent.Check([]string{oldDir}, opt, time.Now())})
	if f := m.recentFiles(); len(f) != 1 || f[0].Path != nf {
		t.Errorf("setelah hapus: %+v", f)
	}

	// Disimpan ke snapshot. Data dibuat setelah pemantauan mulai, jadi lengkap
	// sampai sekarang (Synced maju).
	m.liveSince = m.dataFresh.Add(-time.Second)
	m.liveDirty = true
	m.saveLive()()
	snap, err := recent.LoadSnapshot(recent.SnapshotPath(dataDir()))
	if err != nil || len(snap.Report.Drives[0].Files) != 1 || time.Since(snap.Synced) > time.Minute {
		t.Fatalf("snapshot = %+v, %v", snap, err)
	}

	// Data dari snapshot lama (dibuat sebelum pemantauan mulai) tidak diperpanjang.
	m.liveDirty = true
	m.recentAt = time.Now().Add(-5 * time.Hour)
	m.dataFresh = m.recentAt
	m.liveSince = time.Now()
	m.saveLive()()
	snap, _ = recent.LoadSnapshot(recent.SnapshotPath(dataDir()))
	if time.Since(snap.Fresh()) < 4*time.Hour {
		t.Errorf("snapshot lama ikut diperpanjang: Fresh %v", snap.Fresh())
	}
}

// g / Home melompat ke baris pertama, G / End ke baris terakhir, di setiap
// layar yang punya daftar.
func TestJumpFirstLast(t *testing.T) {
	root := tempDir(t)
	for _, n := range []string{"a", "b", "c", "d"} {
		write(t, filepath.Join(root, n+".txt"), 10)
	}
	m := newModel(sizer.New())
	m.binList = func([]string) ([]bin.Item, error) {
		return []bin.Item{
			{Original: `C:\1.iso`, Info: `C:\$Recycle.Bin\S\$I1`, Size: 3},
			{Original: `C:\2.iso`, Info: `C:\$Recycle.Bin\S\$I2`, Size: 2},
			{Original: `C:\3.iso`, Info: `C:\$Recycle.Bin\S\$I3`, Size: 1},
		}, nil
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(drivesMsg{{Root: `C:\`, Ready: true}, {Root: `D:\`, Ready: true}, {Root: `E:\`, Ready: true}})

	// Daftar drive.
	key(m, "G")
	if m.cursor != 2 {
		t.Errorf("drive: G → %d, want 2", m.cursor)
	}
	key(m, "g")
	if m.cursor != 0 {
		t.Errorf("drive: g → %d, want 0", m.cursor)
	}

	// File baru: g ke baris pertama (judul drive), G ke baris terakhir.
	m.Update(recentMsg{rep: recentReport(3, `C:\`, `D:\`)})
	key(m, "tab") // halaman File baru
	if !m.focusRecent {
		t.Fatal("tab tidak membuka halaman File baru")
	}
	key(m, "down")
	key(m, "G")
	if last := len(recentSelectable(m.recentLines())) - 1; m.rcursor != last {
		t.Errorf("File baru: G → %d, want %d", m.rcursor, last)
	}
	key(m, "g")
	if m.rcursor != 0 {
		t.Errorf("File baru: g → %d, want 0", m.rcursor)
	}
	// Recycle Bin: tab berikutnya.
	key(m, "tab")
	key(m, "G")
	if m.bcursor != 2 {
		t.Errorf("Recycle Bin: G → %d, want 2", m.bcursor)
	}
	key(m, "g") // baris pertama adalah tombol "Kosongkan Recycle Bin"
	if m.bcursor != binButton {
		t.Errorf("Recycle Bin: g → %d, want tombol Kosongkan (%d)", m.bcursor, binButton)
	}
	key(m, "down")
	if m.bcursor != 0 {
		t.Errorf("Recycle Bin: ↓ dari tombol → %d, want item pertama (0)", m.bcursor)
	}

	// Isi folder.
	run(m, loadDir(root))
	settle(t, m)
	key(m, "G")
	if m.cursor != 3 {
		t.Errorf("folder: G → %d, want 3", m.cursor)
	}
	key(m, "g")
	if m.cursor != 0 {
		t.Errorf("folder: g → %d, want 0", m.cursor)
	}
}

func TestEscHome(t *testing.T) {
	root := tempDir(t)
	deep := filepath.Join(root, "a", "b")
	write(t, filepath.Join(deep, "x.txt"), 10)
	m := newModel(sizer.New())
	m.drives = []drives.Drive{{Root: `X:\`, Ready: true}, {Root: filepath.VolumeName(root) + `\`, Ready: true}}
	run(m, loadDir(deep))

	// ← naik satu folder.
	key(m, "left")
	if m.path != filepath.Join(root, "a") {
		t.Fatalf("left: path %s", m.path)
	}
	// esc langsung ke halaman utama, kursor di drive folder tadi.
	key(m, "esc")
	// Daftar drive dibaca ulang dari sistem; kursor tetap di drive folder tadi.
	vol := filepath.VolumeName(root) + `\`
	if m.path != "" || m.cursor >= len(m.drives) || m.drives[m.cursor].Root != vol {
		t.Errorf("esc: path %q, kursor %d; want halaman utama dengan kursor di %s", m.path, m.cursor, vol)
	}
}

func TestDrivesReloadKeepsCursor(t *testing.T) {
	m := newModel(sizer.New())
	ds := drivesMsg{{Root: `C:\`, Ready: true}, {Root: `D:\`, Ready: true}}
	m.Update(ds)
	key(m, "down")
	m.Update(ds)
	if m.cursor != 1 {
		t.Errorf("kursor = %d setelah daftar drive dibaca ulang, want 1", m.cursor)
	}
}

func TestNewMarkers(t *testing.T) {
	root := tempDir(t)
	dl := filepath.Join(root, "dl")
	a, b := filepath.Join(dl, "a.iso"), filepath.Join(dl, "b.zip")
	sub := filepath.Join(dl, "sub", "c.bin")
	for _, p := range []string{a, b, sub, filepath.Join(dl, "lama.txt")} {
		write(t, p, 100)
	}
	now := time.Now()
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{{
		Root: filepath.VolumeName(root) + `\`, Files: []recent.File{
			{Path: b, Size: 100, Created: now.Add(-time.Hour)},
			{Path: sub, Size: 100, Created: now.Add(-2 * time.Hour)},
			{Path: a, Size: 100, Created: now.Add(-3 * time.Hour)},
		},
	}}}})

	run(m, loadDir(dl))
	settle(t, m)
	// File baru ditandai di explorer; subfolder berisi file baru diberi jumlahnya.
	v := m.View()
	for _, want := range []string{"3 file baru di sini (24 jam)", "● baru · " + now.Add(-time.Hour).Format("15:04") + " · 1 jam lalu",
		"● baru · " + now.Add(-3*time.Hour).Format("15:04") + " · 3 jam lalu", "1 file baru"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "lama.txt") && strings.Contains(l, "baru") {
			t.Errorf("file lama ikut ditandai baru: %q", l)
		}
	}
}

func TestTrendSection(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(drivesMsg{{Root: `C:\`, Ready: true, Total: 100 << 30, Free: 50 << 30}})
	if v := m.View(); !strings.Contains(v, "Belum ada riwayat") {
		t.Errorf("tanpa riwayat:\n%s", v)
	}

	now := time.Now()
	const gb = 1 << 30
	h := &trend.History{}
	h.Add(now.Add(-8*24*time.Hour), map[string]int64{`C:\proj\cache`: 1 * gb, `C:\proj`: 2 * gb})
	h.Add(now.Add(-3*time.Hour), map[string]int64{`C:\proj\cache`: 4 * gb, `C:\proj`: 5 * gb})
	m.Update(snapshotMsg{hist: h})
	v := m.View() // bawaan 7 hari
	for _, want := range []string{"Tren", "7 hari", "+3.0 GB", `C:\proj\cache`} {
		if !strings.Contains(v, want) {
			t.Errorf("7 hari: View tidak memuat %q:\n%s", want, v)
		}
	}
	key(m, "1") // 24 jam: riwayat 3 jam belum cukup
	if v := m.View(); !strings.Contains(v, "Belum cukup riwayat untuk 24 jam") {
		t.Errorf("24 jam:\n%s", v)
	}
	key(m, "3")
	if m.trange != rangeMonth {
		t.Errorf("3: rentang %v", m.trange)
	}
}

func TestTrendSelect(t *testing.T) {
	root := tempDir(t)
	cache := filepath.Join(root, "cache")
	write(t, filepath.Join(cache, "x.bin"), 10)
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(drivesMsg{{Root: `C:\`, Ready: true}, {Root: `D:\`, Ready: true}})
	const gb = 1 << 30
	h := &trend.History{}
	now := time.Now()
	h.Add(now.Add(-8*24*time.Hour), map[string]int64{cache: 1 * gb, `C:\gone`: 1 * gb})
	h.Add(now.Add(-time.Hour), map[string]int64{cache: 4 * gb, `C:\gone`: 3 * gb})
	m.Update(snapshotMsg{hist: h})
	m.View() // menggambar: jumlah baris Tren diketahui

	key(m, "down") // drive kedua
	key(m, "down") // masuk ke daftar Tren
	if !m.trendFocus || m.tcursor != 0 {
		t.Fatalf("↓ dari drive terakhir: fokus %v, kursor %d", m.trendFocus, m.tcursor)
	}
	if v := m.View(); !strings.Contains(v, "enter masuk · a analisis") {
		t.Errorf("petunjuk tombol Tren tidak tampil:\n%s", v)
	}
	key(m, "down")
	if m.tcursor != 1 {
		t.Errorf("↓ di Tren: kursor %d", m.tcursor)
	}
	key(m, "up")
	g, _ := m.trendSelected() // baris pertama: pertambahan terbesar (cache atau gone, sama-sama +2 GB)
	if g.Delta < 2*gb-trend.MinDir {
		t.Errorf("baris terpilih: %+v", g)
	}
	// Folder yang masih ada dibuka; yang sudah hilang memberi tahu.
	m.tcursor = slices.IndexFunc(m.trendRows, func(g trend.Growth) bool { return g.Path == `C:\gone` })
	key(m, "enter")
	if m.path != "" || !strings.Contains(m.status, "sudah tidak ada") {
		t.Errorf("folder hilang: path %q status %q", m.path, m.status)
	}
	m.tcursor = slices.IndexFunc(m.trendRows, func(g trend.Growth) bool { return g.Path == cache })
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if m.path != cache {
		t.Errorf("enter: path %q, want %q", m.path, cache)
	}
}
