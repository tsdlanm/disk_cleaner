package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

type (
	recentMsg struct {
		rep  *recent.Report
		dirs map[string]sizer.Info
		at   time.Time // waktu pemindaian
		err  error
	}
	snapshotMsg   struct{ snap *recent.Snapshot } // nil jika belum ada atau rusak
	recentTickMsg struct{}
)

// dataDir adalah folder data aplikasi (%LOCALAPPDATA%\scan), atau "" jika
// tidak tersedia.
func dataDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "scan")
	}
	return ""
}

// loadSnapshot membaca hasil pemindaian terakhir yang tersimpan.
func loadSnapshot() tea.Msg {
	base := dataDir()
	if base == "" {
		return snapshotMsg{}
	}
	snap, err := recent.LoadSnapshot(recent.SnapshotPath(base))
	if err != nil {
		return snapshotMsg{}
	}
	return snapshotMsg{snap: snap}
}

// rescanAge adalah umur hasil tersimpan yang masih dipakai tanpa memindai ulang.
func (m *model) rescanAge() time.Duration {
	h := m.cfg.RescanHours
	if h <= 0 {
		h = 6
	}
	return time.Duration(h) * time.Hour
}

// days adalah rentang "File baru" dari konfigurasi.
func (m *model) days() int {
	if m.cfg.RecentDays > 0 {
		return m.cfg.RecentDays
	}
	return recent.DefaultOptions.Days
}

// recentOptions adalah pengaturan pemindaian dan pemeriksaan file baru.
func (m *model) recentOptions() recent.Options {
	opt := recent.Options{Days: m.days(), MinFile: m.cfg.RecentMinKB << 10}
	if base := dataDir(); base != "" {
		opt.Ignore = []string{base}
	}
	return opt
}

// startRecent memindai semua drive lokal di background.
func (m *model) startRecent() tea.Cmd {
	roots := m.scanRoots()
	if len(roots) == 0 || m.recentRunning {
		return nil
	}
	if m.recentCancel != nil {
		m.recentCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.recentCancel = cancel
	m.recentRunning, m.recentProg, m.recentErr = true, &recent.Progress{}, nil
	m.liveBuf = nil
	// Ukuran setiap folder yang dihitung pemindaian ini langsung masuk ke cache
	// explorer (jadi membuka folder setelahnya tidak memindai ulang) dan ikut
	// disimpan di snapshot untuk dipakai saat program dibuka lagi.
	var dmu sync.Mutex
	dirs := make(map[string]sizer.Info)
	sz := m.sz
	opt := m.recentOptions()
	opt.OnDir = func(path string, inf sizer.Info) {
		sz.Put(path, inf)
		if inf.Size >= recent.MinSnapshotDir {
			dmu.Lock()
			dirs[path] = inf
			dmu.Unlock()
		}
	}
	base := dataDir()
	prog := m.recentProg
	var rootList []recent.Root
	for _, d := range roots {
		rootList = append(rootList, recent.Root{Path: d.Root, Used: d.Used()})
	}
	scan := func() tea.Msg {
		start := time.Now()
		rep, err := recent.Scan(ctx, rootList, opt, base, prog)
		if err == nil && base != "" {
			_ = recent.SaveSnapshot(recent.SnapshotPath(base), &recent.Snapshot{Time: start, Report: rep, Dirs: dirs})
		}
		return recentMsg{rep: rep, dirs: dirs, at: start, err: err}
	}
	return tea.Batch(scan, recentTick())
}

func recentTick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return recentTickMsg{} })
}

func (m *model) updateRecent(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case snapshotMsg:
		// Hasil tersimpan langsung ditampilkan; pemindaian penuh hanya jika
		// belum ada hasil atau hasilnya sudah terlalu lama.
		if s := msg.snap; s != nil && s.Report != nil {
			m.setRecent(s.Report, s.Time)
			m.snapDirs, m.dataFresh = s.Dirs, s.Fresh()
			for p, inf := range s.Dirs {
				m.sz.Put(p, inf)
			}
			if time.Since(s.Fresh()) < m.rescanAge() {
				return nil
			}
		}
		return m.startRecent()

	case recentMsg:
		m.recentRunning = false
		if msg.err != nil {
			m.recentErr = msg.err
			return nil
		}
		m.setRecent(msg.rep, msg.at)
		m.snapDirs, m.dataFresh = msg.dirs, msg.at
		// Perubahan selama pemindaian berjalan bisa saja terlewat olehnya.
		if len(m.liveBuf) > 0 && m.recent.Apply(m.liveBuf) {
			m.liveDirty = true
		}
		m.liveBuf, m.liveLost = nil, false
	case recentTickMsg:
		if m.recentRunning {
			return recentTick()
		}
	}
	return nil
}

func (m *model) setRecent(rep *recent.Report, at time.Time) {
	m.recent, m.recentAt = rep, at
	m.liveSaved = time.Now() // data ini baru dimuat/disimpan
	m.recentShown = make(map[string]int)
	m.rcursor, m.roffset = 0, 0
	m.recentChanged()
}

// applyRecent menerapkan perubahan ke "File baru" dengan kursor tetap di
// folder yang sama. Mengembalikan true jika ada yang berubah.
func (m *model) applyRecent(changes []recent.Change) bool {
	if m.recent == nil {
		return false
	}
	sel := m.selectedRecent()
	if !m.recent.Apply(changes) {
		return false
	}
	m.liveDirty = true
	m.recentChanged()
	m.restoreRecent(sel)
	return true
}

// recentChanged membuang daftar folder yang sudah disusun setelah datanya berubah.
func (m *model) recentChanged() {
	m.rgroups, m.rnew = nil, nil
}

// newIndex menandai file baru di explorer: waktu dibuat setiap file baru, dan
// jumlah file baru di setiap folder induknya (semua tingkat).
type newIndex struct {
	files map[string]time.Time
	dirs  map[string]int
}

// newFiles mengembalikan indeks file baru dalam rentang "File baru".
func (m *model) newFiles() *newIndex {
	if m.rnew != nil {
		return m.rnew
	}
	x := &newIndex{files: make(map[string]time.Time), dirs: make(map[string]int)}
	m.rnew = x
	if m.recent == nil {
		return x
	}
	since := time.Now().AddDate(0, 0, -m.days())
	for _, d := range m.recent.Drives {
		for _, f := range d.Files {
			if f.Created.Before(since) {
				break // urut dari yang terbaru
			}
			x.files[f.Path] = f.Created
			for p := filepath.Dir(f.Path); ; p = filepath.Dir(p) {
				x.dirs[p]++
				if filepath.Dir(p) == p {
					break
				}
			}
		}
	}
	return x
}

// shortFolder menyingkat folder di dalam profil pengguna, misalnya
// "C:\Users\nama\Downloads" menjadi "Downloads".
func shortFolder(dir string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "folder pengguna"
			}
			return rel
		}
	}
	return dir
}

// relTime memformat waktu seperti "2 jam lalu".
func relTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "baru saja"
	case d < time.Hour:
		return fmt.Sprintf("%d menit lalu", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d jam lalu", int(d.Hours()))
	case d < 48*time.Hour:
		return "kemarin"
	}
	return fmt.Sprintf("%d hari lalu", int(d.Hours()/24))
}

// truncateLeft memotong bagian awal teks agar ujung path tetap terlihat.
func truncateLeft(s string, w int) string {
	if runewidth.StringWidth(s) <= w {
		return s
	}
	r := []rune(s)
	for runewidth.StringWidth(string(r)) > w-1 {
		r = r[1:]
	}
	return "…" + string(r)
}
