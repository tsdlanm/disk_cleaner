package ui

import (
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsdlanm/cli_app/internal/drives"
	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/watch"
)

// liveMsg berisi perubahan file yang dilaporkan Windows, sudah diperiksa
// keadaannya (lihat recent.Check).
type liveMsg struct {
	changes []recent.Change
	dirs    map[string]bool // folder yang isinya berubah langsung
	lost    bool
}

const (
	// liveSaveEvery: perubahan dari pemantauan disimpan ke snapshot paling
	// sering sekali dalam rentang ini (dan saat program ditutup).
	liveSaveEvery = 2 * time.Minute
	// drivesEvery: ruang bebas drive dibaca ulang paling sering sekali dalam
	// rentang ini selama ada perubahan file.
	drivesEvery = 3 * time.Second
	// maxLiveBuf membatasi perubahan yang ditampung selama pemindaian penuh.
	maxLiveBuf = 50000
)

// scanRoots adalah drive yang dipindai dan dipantau: drive lokal dan USB yang siap.
func (m *model) scanRoots() []drives.Drive {
	var out []drives.Drive
	for _, d := range m.drives {
		if d.Ready && (d.Kind == "Lokal" || d.Kind == "USB") {
			out = append(out, d)
		}
	}
	return out
}

// startLive mulai memantau perubahan di semua drive, agar "File baru",
// ruang bebas drive, dan isi folder yang sedang dibuka selalu terbaru tanpa
// memindai ulang.
func (m *model) startLive() tea.Cmd {
	if m.watcher != nil {
		return nil
	}
	var roots []string
	for _, d := range m.scanRoots() {
		roots = append(roots, d.Root)
	}
	if len(roots) == 0 {
		return nil
	}
	w, err := watch.Start(roots)
	if err != nil {
		m.status = "Pemantauan langsung tidak aktif: " + err.Error()
		return nil
	}
	m.watcher, m.liveSince = w, time.Now()
	return m.waitLive()
}

// waitLive menunggu batch perubahan berikutnya lalu memeriksanya di
// background, agar UI tidak tertahan oleh syscall per file.
func (m *model) waitLive() tea.Cmd {
	if m.watcher == nil {
		return nil
	}
	w, opt := m.watcher, m.recentOptions()
	return func() tea.Msg {
		b, ok := <-w.C
		if !ok {
			return nil
		}
		dirs := make(map[string]bool)
		for _, p := range b.Paths {
			dirs[filepath.Dir(p)] = true
		}
		return liveMsg{changes: recent.Check(b.Paths, opt, time.Now()), dirs: dirs, lost: b.Lost}
	}
}

func (m *model) updateLive(msg liveMsg) tea.Cmd {
	cmds := []tea.Cmd{m.waitLive()}
	if msg.lost {
		m.liveLost = true
	}
	m.applyRecent(msg.changes)
	if m.recentRunning && len(m.liveBuf) < maxLiveBuf {
		m.liveBuf = append(m.liveBuf, msg.changes...)
	}
	// Isi folder yang sedang dibuka ikut berubah: muat ulang (kursor tetap).
	if m.path != "" && msg.dirs[filepath.Clean(m.path)] {
		cmds = append(cmds, loadDir(m.path))
	}
	if time.Since(m.drivesAt) >= drivesEvery {
		m.drivesAt = time.Now()
		cmds = append(cmds, loadDrives)
	}
	if time.Since(m.liveSaved) >= liveSaveEvery {
		cmds = append(cmds, m.saveLive())
	}
	return tea.Batch(cmds...)
}

// saveLive menyimpan "File baru" yang sudah diperbarui pemantauan ke
// snapshot, agar saat program dibuka lagi datanya tetap terbaru. Mengembalikan
// nil jika tidak ada yang perlu disimpan.
func (m *model) saveLive() tea.Cmd {
	base := dataDir()
	if !m.liveDirty || m.recent == nil || m.recentRunning || base == "" {
		return nil
	}
	snap := &recent.Snapshot{Time: m.recentAt, Report: m.recent.Clone(), Dirs: m.snapDirs, Synced: m.dataFresh}
	if !m.liveLost && !m.dataFresh.Before(m.liveSince) {
		// Pemantauan sudah berjalan sejak data ini dibuat dan tidak ada yang
		// terlewat: data lengkap sampai sekarang. Data dari snapshot lama tidak
		// diperpanjang, karena perubahan selama program tertutup tidak terpantau.
		snap.Synced = time.Now()
	}
	m.liveDirty, m.liveSaved = false, time.Now()
	path := recent.SnapshotPath(base)
	return func() tea.Msg {
		_ = recent.SaveSnapshot(path, snap)
		return nil
	}
}

// stopLive menghentikan pemantauan dan menyimpan perubahan terakhir.
func (m *model) stopLive() {
	if m.watcher == nil {
		return
	}
	m.watcher.Close()
	if cmd := m.saveLive(); cmd != nil {
		cmd()
	}
}
