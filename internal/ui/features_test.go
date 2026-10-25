package ui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsdlanm/cli_app/internal/drives"
	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

func TestLiveUpdate(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	vol := filepath.VolumeName(root) + `\`
	oldDir, newDir := filepath.Join(root, "lama"), filepath.Join(root, "unduhan")
	old := filepath.Join(oldDir, "a.bin")
	write(t, old, 2<<20)

	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{{
		Root: vol, Files: []recent.File{{Path: old, Size: 2 << 20, Created: time.Now().Add(-time.Hour)}},
	}}}, at: time.Now()})
	// File tes ada di folder Temp (label cache): buka bagian cache dulu.
	m.rexpand = map[string]bool{vol: true}
	key(m, "tab")
	key(m, "down")
	if m.selectedRecent() != oldDir {
		t.Fatalf("kursor = %q", m.selectedRecent())
	}
	run(m, loadDir(root))
	before := len(m.rows)

	// File baru di folder lain: foldernya masuk di atas, kursor tetap di
	// folder yang sama, dan folder yang sedang dibuka ikut dimuat ulang.
	nf := filepath.Join(newDir, "x.iso")
	write(t, nf, 3<<20)
	opt := m.recentOptions()
	_, cmd := m.Update(liveMsg{changes: recent.Check([]string{nf, newDir}, opt, time.Now()), dirs: map[string]bool{root: true, newDir: true}})
	if g := allGroups(m); len(g) != 2 || g[0].dir != newDir {
		t.Fatalf("groups = %+v", g)
	}
	if m.selectedRecent() != oldDir {
		t.Errorf("kursor pindah ke %q", m.selectedRecent())
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
	if g := allGroups(m); len(g) != 1 || g[0].dir != newDir {
		t.Errorf("setelah hapus: %+v", g)
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

func TestEscHome(t *testing.T) {
	root := t.TempDir()
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
	root := t.TempDir()
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

	// Folder dl adalah yang terbaru; enter membukanya dengan kursor di file terbarunya.
	m.rexpand = map[string]bool{filepath.VolumeName(root) + `\`: true} // folder tes berlabel cache
	key(m, "tab")
	key(m, "down")
	key(m, "enter")
	if m.path != dl || m.rows[m.cursor].name != "b.zip" {
		t.Fatalf("enter: path %s, kursor di %q", m.path, m.rows[m.cursor].name)
	}
	settle(t, m)
	v := m.View()
	for _, want := range []string{"3 file baru di sini", "● baru · 1 jam lalu", "● baru · 3 jam lalu", "1 file baru"} {
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

func TestRecentScrollKeepsDriveTitle(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 12})
	m.Update(recentMsg{rep: recentReport(25, `C:\`, `D:\`)})
	key(m, "tab")
	for range 8 {
		key(m, "down")
	}
	if strings.Contains(m.View(), `C:\ · 25 folder`) {
		t.Fatal("daftar tidak tergulir; tes tidak menguji apa-apa")
	}
	for range 8 {
		key(m, "up")
	}
	if v := m.View(); !strings.Contains(v, `C:\ · 25 folder`) {
		t.Errorf("judul drive C hilang saat kursor di folder pertamanya:\n%s", v)
	}
}

// allGroups mengembalikan semua folder di drive pertama, pengguna maupun
// cache (file di folder Temp tes berlabel cache), urut dari yang terbaru.
func allGroups(m *model) []folderGroup {
	g := m.groups(0)
	all := append(append([]folderGroup{}, g.user...), g.other...)
	slices.SortFunc(all, func(a, b folderGroup) int { return b.newest.Compare(a.newest) })
	return all
}
