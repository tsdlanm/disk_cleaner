// Package ui adalah TUI file explorer yang menampilkan ukuran folder.
package ui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/analyze"

	"github.com/tsdlanm/cli_app/internal/drives"
	"github.com/tsdlanm/cli_app/internal/format"

	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/sizer"
	"github.com/tsdlanm/cli_app/internal/trash"
	"github.com/tsdlanm/cli_app/internal/watch"
)

const tickInterval = 100 * time.Millisecond

// row adalah satu item di dalam folder yang sedang dibuka.
type row struct {
	name   string
	path   string
	isDir  bool
	link   bool // symlink atau junction; tidak dihitung ukurannya
	size   int64
	state  sizer.State
	denied bool
}

type (
	drivesMsg []drives.Drive
	dirMsg    struct {
		path string
		rows []row
		err  error
	}
	tickMsg   struct{}
	deleteMsg struct {
		paths  []string
		failed int // jumlah item yang masih ada setelah dihapus
		err    error
	}
)

type model struct {
	sz     *sizer.Sizer
	drives []drives.Drive

	path    string // "" berarti layar daftar drive
	rows    []row
	total   int64
	pending int // jumlah folder di layar yang ukurannya belum selesai

	cursor, offset int
	follow         bool // kursor mengikuti item saat diurutkan ulang (false: tetap di posisinya)
	byName         bool
	width, height  int

	remember map[string]string // folder -> nama item yang terakhir dipilih
	status   string
	ticking  bool
	admin    bool
	popup    *popup // popup yang sedang terbuka, nil jika tidak ada
	elevate  bool   // pengguna meminta restart sebagai Administrator
	start    string // folder yang dibuka saat mulai

	marks map[string]bool      // path item yang ditandai di folder ini
	trash func([]string) error // memindahkan item ke Recycle Bin

	cfg         analyze.Config
	newProvider func(analyze.Config) (analyze.Provider, error)
	an          *analysis // layar analisis AI yang sedang terbuka
	lastAn      *analysis // analisis terakhir, bisa dibuka lagi dengan b
	anSeq       int       // nomor analisis terakhir; pesan dari analisis lama diabaikan
	settings    *settings // layar Pengaturan AI yang sedang terbuka

	autoRecent    bool // pindai file baru saat daftar drive pertama kali dimuat
	recentStarted bool
	recentRunning bool
	recentCancel  context.CancelFunc
	recentProg    *recent.Progress
	recent        *recent.Report
	recentErr     error
	recentShown   map[string]int // jumlah folder yang ditampilkan per drive
	recentAt      time.Time      // waktu pemindaian data yang sedang tampil

	focusRecent bool // kursor ada di bagian "File baru"
	rcursor     int
	roffset     int
	rgroups     map[int]driveGroups   // folder berisi file baru per drive; nil = susun ulang
	rexpand     map[string]bool       // drive yang bagian cache/aplikasi/sistem-nya dibuka
	rnew        *newIndex             // penanda file baru di explorer; nil = susun ulang
	snapDirs    map[string]sizer.Info // ukuran folder dari pemindaian, untuk snapshot
	dataFresh   time.Time             // data "File baru" lengkap sampai saat ini

	// background: saat mulai, nyalakan pemantauan langsung (dimatikan di tes).
	background bool
	watcher    *watch.Watcher
	liveSince  time.Time // pemantauan berjalan sejak
	liveLost   bool      // sebagian perubahan terlewat
	liveDirty  bool      // ada perubahan yang belum disimpan ke snapshot
	liveSaved  time.Time
	liveBuf    []recent.Change // perubahan selama pemindaian penuh berjalan
	drivesAt   time.Time       // terakhir ruang drive dibaca ulang karena ada perubahan
}

// popup menampilkan informasi (analisis AI) atau konfirmasi hapus.
type popup struct {
	title   string
	lines   []string
	confirm []string // path yang dihapus jika dikonfirmasi; nil untuk popup info
	hint    string   // teks tombol di bawah popup; kosong memakai bawaan
}

// Options mengatur TUI saat dijalankan.
type Options struct {
	Admin     bool   // program berjalan dengan hak Administrator
	Status    string // pesan awal di baris status
	StartPath string // folder yang langsung dibuka; "" berarti daftar drive
}

// Result menjelaskan kenapa TUI berhenti.
type Result struct {
	Elevate bool   // pengguna menekan A untuk restart sebagai Administrator
	Path    string // folder yang sedang dibuka saat berhenti
}

// Run menjalankan TUI sampai pengguna keluar.
func Run(opts Options) (Result, error) {
	m := newModel(sizer.New())
	m.admin, m.status, m.start = opts.Admin, opts.Status, opts.StartPath
	cfg, err := analyze.LoadConfig()
	if err != nil {
		m.status = "Konfigurasi AI tidak terbaca, memakai bawaan: " + err.Error()
	}
	m.cfg = cfg
	m.autoRecent, m.background = true, true

	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	m.stopLive()
	if err != nil {
		return Result{}, err
	}
	return Result{Elevate: m.elevate, Path: m.path}, nil
}

func newModel(sz *sizer.Sizer) *model {
	return &model{
		sz: sz, remember: make(map[string]string), marks: make(map[string]bool), trash: trash.Move,
		cfg: analyze.DefaultConfig, newProvider: analyze.Config.NewProvider,
	}
}

func (m *model) Init() tea.Cmd {
	if m.start != "" {
		return tea.Batch(loadDrives, loadDir(m.start))
	}
	return loadDrives
}

func loadDrives() tea.Msg {
	return drivesMsg(drives.List())
}

// loadDir membaca isi folder. Ukuran file langsung didapat dari ReadDir,
// ukuran subfolder diisi kemudian oleh sizer.
func loadDir(path string) tea.Cmd {
	return func() tea.Msg {
		rows, err := readDir(path)
		return dirMsg{path: path, rows: rows, err: err}
	}
}

// readDir membaca isi folder sebagai baris.
func readDir(path string) ([]row, error) {
	entries, err := os.ReadDir(path)
	if err != nil && len(entries) == 0 {
		return nil, err
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		r := row{name: e.Name(), path: filepath.Join(path, e.Name())}
		switch {
		case sizer.IsRealDir(e):
			r.isDir = true
		case e.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
			r.link = true
			r.state = sizer.Done
			if fi, err := os.Stat(r.path); err == nil {
				r.isDir = fi.IsDir()
			}
		default:
			r.state = sizer.Done
			if fi, err := e.Info(); err == nil {
				r.size = fi.Size()
			}
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case drivesMsg:
		// Kursor tetap di drive yang sama saat daftar dibaca ulang.
		sel := m.remember[""]
		if len(m.drives) > 0 && m.path == "" && m.cursor < len(m.drives) {
			sel = m.drives[m.cursor].Root
		}
		m.drives = msg
		if m.path == "" {
			m.cursor = m.indexOfDrive(sel)
		}

		if m.autoRecent && !m.recentStarted {
			m.recentStarted = true
			// Hasil pemindaian tersimpan dimuat dulu; pemindaian penuh hanya
			// jika belum ada atau sudah terlalu lama (lihat updateRecent).
			cmds := []tea.Cmd{loadSnapshot}
			if m.background {
				cmds = append(cmds, m.startLive())
			}
			cmd = tea.Batch(cmds...)
		}

	case recentMsg, snapshotMsg, recentTickMsg:
		cmd = m.updateRecent(msg)

	case liveMsg:
		cmd = m.updateLive(msg)

	case dirMsg:
		if msg.err != nil {
			m.status = "Tidak bisa membuka " + msg.path + ": " + errText(msg.err)
			break
		}
		if msg.path == m.path {
			// Muat ulang folder yang sama (refresh/setelah hapus): posisi
			// kursor dipertahankan dan tanda untuk item yang sudah hilang dibuang.
			m.rows = msg.rows
			m.refresh()
			for p := range m.marks {
				if !slices.ContainsFunc(m.rows, func(r row) bool { return r.path == p }) {
					delete(m.marks, p)
				}
			}
			cmd = m.startTick()
			break
		}
		m.path, m.rows, m.status = msg.path, msg.rows, ""
		m.cursor, m.offset, m.follow = 0, 0, false
		clear(m.marks)
		m.refresh()
		if i := m.indexOfRow(m.remember[m.path]); i >= 0 {
			m.cursor, m.follow = i, true
		}
		cmd = m.startTick()

	case snapMsg, aiMsg, anTickMsg:
		cmd = m.updateAnalysis(msg)

	case deleteMsg:

		for _, p := range msg.paths {
			m.sz.Invalidate(p)
			if _, err := os.Lstat(p); err != nil {
				for _, an := range []*analysis{m.an, m.lastAn} {
					if an != nil {
						an.gone[p] = true
						delete(an.marks, p)
					}
				}
			}
		}
		// Tanpa menunggu pemantauan: item yang terhapus langsung hilang dari "File baru".
		m.applyRecent(recent.Check(msg.paths, m.recentOptions(), time.Now()))
		switch {
		case msg.failed == 0:
			m.status = fmt.Sprintf("%d item dipindahkan ke Recycle Bin", len(msg.paths))
		case msg.err != nil:
			m.status = fmt.Sprintf("%d dari %d item gagal dihapus: %v", msg.failed, len(msg.paths), msg.err)
		default:
			m.status = fmt.Sprintf("%d dari %d item gagal dihapus", msg.failed, len(msg.paths))
		}
		cmd = loadDrives
		if m.path != "" {
			cmd = tea.Batch(cmd, loadDir(m.path))
		}
		cmd = tea.Batch(cmd, m.reloadExp())

	case tickMsg:
		m.ticking = false
		if m.path != "" {
			m.refresh()
		}
		m.refreshExp()
		cmd = m.startTick()

	case expMsg:
		cmd = m.updateExp(msg)

	case tea.KeyMsg:
		if m.settings != nil {
			cmd = m.handleSettingsKey(msg)
		} else {
			cmd = m.handleKey(msg.String())
		}
	}
	m.clamp()
	return m, cmd
}

func (m *model) handleKey(key string) tea.Cmd {
	if m.popup != nil {
		if p := m.popup; p.confirm != nil {
			switch key {
			case "ctrl+c":
				return tea.Quit
			case "y", "Y":
				m.popup = nil
				m.status = fmt.Sprintf("Memindahkan %d item ke Recycle Bin…", len(p.confirm))
				return m.deleteCmd(p.confirm)
			case "n", "N", "esc", "q":
				m.popup = nil
			}
			return nil
		}
		switch key {
		case "ctrl+c":
			return tea.Quit
		case "esc", "enter", "q":
			m.popup = nil
		}
		return nil
	}
	if m.an != nil {
		return m.handleAnalysisKey(key)
	}
	m.status = ""
	if m.path == "" && m.focusRecent {
		if cmd, handled := m.handleRecentKey(key); handled {
			return cmd
		}
	}
	if m.path == "" && key == "tab" {
		m.focusRecent = m.hasRecentFiles()
		return nil
	}
	switch key {
	case "up", "k", "down", "j", "pgup", "pgdown", "home", "g", "end", "G":
		m.follow = true
	}
	switch key {
	case "ctrl+c", "q":
		return tea.Quit
	case "up", "k":
		// Dari item pertama melompat ke item terakhir.
		if m.cursor == 0 {
			m.cursor = m.count() - 1
		} else {
			m.cursor--
		}
	case "down", "j":
		// Dari item terakhir kembali ke item pertama.
		if m.cursor >= m.count()-1 {
			m.cursor = 0
		} else {
			m.cursor++
		}
	case "pgup":
		m.cursor -= m.listHeight()
	case "pgdown":
		m.cursor += m.listHeight()
	case "home", "g":
		m.cursor = 0
	case "end", "G":
		m.cursor = m.count() - 1
	case "enter", "right", "l":
		return m.open()
	case "backspace", "left", "h":
		return m.up()
	case "esc":
		return m.home()
	case "s":
		if m.path != "" {
			m.byName = !m.byName
			m.refresh()
		}
	case "r":
		if m.path == "" {
			return tea.Batch(loadDrives, m.startRecent())
		}
		m.sz.Invalidate(m.path)
		return loadDir(m.path)
	case " ", "space":
		if m.path != "" && m.cursor < len(m.rows) {
			p := m.rows[m.cursor].path
			if m.marks[p] {
				delete(m.marks, p)
			} else {
				m.marks[p] = true
			}
			m.follow = true
			m.cursor++
		}
	case "delete", "d":
		m.popup = m.deletePopup()
	case "o":
		m.openExplorer()
	case "a":
		return m.requestAnalysis()
	case "p":
		m.openSettings("", false)

	case "b":
		if m.lastAn == nil {
			m.status = "Belum ada hasil analisis AI"
			return nil
		}
		m.an = m.lastAn
	case "A":
		if m.admin {
			m.status = "Sudah berjalan sebagai Administrator"
			return nil
		}
		m.elevate = true
		return tea.Quit
	}
	return nil
}

// deleteTargets mengembalikan item yang ditandai, atau item di kursor jika
// tidak ada yang ditandai.
func (m *model) deleteTargets() []row {
	var out []row
	for _, r := range m.rows {
		if m.marks[r.path] {
			out = append(out, r)
		}
	}
	if len(out) == 0 && m.cursor < len(m.rows) {
		out = append(out, m.rows[m.cursor])
	}
	return out
}

// deletePopup menyusun popup konfirmasi untuk memindahkan item ke Recycle Bin.
func (m *model) deletePopup() *popup {
	if m.path == "" {
		m.status = "Pilih file atau folder di dalam drive untuk dihapus"
		return nil
	}
	var items []delItem
	for _, r := range m.deleteTargets() {
		items = append(items, delItem{name: r.name, path: r.path, isDir: r.isDir, size: r.size, sizeText: sizeText(r)})
	}
	return m.confirmDelete(items)
}

// delItem adalah satu item di popup konfirmasi hapus.
type delItem struct {
	name, path, sizeText string
	isDir                bool
	size                 int64
}

// confirmDelete menyusun popup konfirmasi untuk memindahkan items ke Recycle Bin.
func (m *model) confirmDelete(items []delItem) *popup {
	if len(items) == 0 {
		return nil
	}
	const maxList = 8
	var lines []string
	var total int64
	paths := make([]string, len(items))
	for i, it := range items {
		paths[i] = it.path
		total += it.size
		if i < maxList {
			name := it.name
			if it.isDir {
				name += string(filepath.Separator)
			}
			name = runewidth.FillRight(runewidth.Truncate(name, 32, "…"), 32)
			lines = append(lines, fmt.Sprintf("  %s %12s", name, it.sizeText))
		}
	}
	if len(items) > maxList {
		lines = append(lines, fmt.Sprintf("  … dan %d item lainnya", len(items)-maxList))
	}
	lines = append(lines, "", "Total   : "+format.Bytes(total))
	if d := m.driveOf(paths[0]); d >= 0 && m.drives[d].Kind != "Lokal" {
		lines = append(lines, "", "Peringatan: drive "+m.drives[d].Kind+" biasanya tidak punya Recycle Bin;",
			"Windows akan meminta konfirmasi untuk menghapus permanen.")
	}
	lines = append(lines, "", "Ruang drive baru bertambah setelah Recycle Bin dikosongkan.")

	return &popup{
		title:   fmt.Sprintf("Pindahkan %d item ke Recycle Bin?", len(items)),
		lines:   lines,
		confirm: paths,
	}
}

// deleteCmd memindahkan paths ke Recycle Bin di background, lalu menghitung
// item yang masih ada (gagal dihapus).
func (m *model) deleteCmd(paths []string) tea.Cmd {
	move := m.trash
	return func() tea.Msg {
		err := move(paths)
		failed := 0
		for _, p := range paths {
			if _, err := os.Lstat(p); err == nil {
				failed++
			}
		}
		return deleteMsg{paths: paths, failed: failed, err: err}
	}
}

// marked mengembalikan jumlah dan total ukuran item yang ditandai.
func (m *model) marked() (n int, size int64) {
	for _, r := range m.rows {
		if m.marks[r.path] {
			n++
			size += r.size
		}
	}
	return n, size
}

// open masuk ke drive atau folder yang dipilih.
func (m *model) open() tea.Cmd {
	if m.path == "" {
		if m.cursor >= len(m.drives) {
			return nil
		}
		d := m.drives[m.cursor]
		if !d.Ready {
			m.status = "Drive " + d.Root + " tidak siap"
			return nil
		}
		m.remember[""] = d.Root
		return loadDir(d.Root)
	}
	if m.cursor >= len(m.rows) {
		return nil
	}
	r := m.rows[m.cursor]
	switch {
	case r.denied:
		m.status = "Akses ke " + r.name + " ditolak"
		return nil
	case !r.isDir:
		m.status = "Tekan o untuk membuka di Explorer"
		return nil
	}
	m.remember[m.path] = r.name
	return loadDir(r.path)
}

// up kembali ke folder induk, atau ke daftar drive dari root sebuah drive.
func (m *model) up() tea.Cmd {
	if m.path == "" {
		return nil
	}
	m.remember[m.path] = m.selectedName()
	parent := filepath.Dir(m.path)
	if parent == m.path {
		m.path, m.rows = "", nil
		m.cursor = m.indexOfDrive(m.remember[""])
		return loadDrives
	}
	m.remember[parent] = filepath.Base(m.path)
	return loadDir(parent)
}

// home kembali ke layar daftar drive dari folder mana pun, dengan kursor di
// drive folder tersebut.
func (m *model) home() tea.Cmd {
	if m.path == "" {
		return nil
	}
	m.remember[m.path] = m.selectedName()
	if d := m.driveOf(m.path); d >= 0 {
		m.remember[""] = m.drives[d].Root
	}
	m.path, m.rows = "", nil
	clear(m.marks)
	m.cursor = m.indexOfDrive(m.remember[""])
	return loadDrives
}

// refresh mengisi ukuran subfolder dari sizer, menjadwalkan yang belum
// dihitung, lalu mengurutkan ulang. Jika follow, kursor tetap di item yang sama.
func (m *model) refresh() {
	sel := m.selectedName()
	m.total, m.pending = fillSizes(m.sz, m.rows)
	sortRows(m.rows, m.byName)
	if !m.follow {
		return
	}
	if i := m.indexOfRow(sel); i >= 0 {
		m.cursor = i
	}
}

// fillSizes mengisi ukuran subfolder dari sizer dan menjadwalkan yang belum
// dihitung. Mengembalikan total ukuran dan jumlah folder yang belum selesai.
func fillSizes(sz *sizer.Sizer, rows []row) (total int64, pending int) {
	var request []string
	for i := range rows {
		r := &rows[i]
		if r.isDir && !r.link {
			inf, st := sz.Get(r.path)
			r.size, r.state, r.denied = inf.Size, st, inf.Denied
			if st == sizer.Unknown {
				request = append(request, r.path)
			}
			if st != sizer.Done {
				pending++
			}
		}
		total += r.size
	}
	if len(request) > 0 {
		sz.Request(request...)
	}
	return total, pending
}

// sortRows mengurutkan baris dari yang terbesar, atau menurut nama
// (folder lebih dulu) jika byName.
func sortRows(rows []row, byName bool) {
	slices.SortStableFunc(rows, func(a, b row) int {
		if byName {
			if a.isDir != b.isDir {
				if a.isDir {
					return -1
				}
				return 1
			}
			return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
		}
		if a.size != b.size {
			if a.size > b.size {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
}

func (m *model) startTick() tea.Cmd {
	if m.ticking || m.pending+m.expPending() == 0 {
		return nil
	}
	m.ticking = true
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) openExplorer() {
	switch {
	case m.path == "" && m.cursor < len(m.drives):
		m.status = runExplorer(`explorer.exe "` + m.drives[m.cursor].Root + `"`)
	case m.path != "" && m.cursor < len(m.rows):
		m.status = explorerSelect(m.rows[m.cursor].path)
	}
}

// explorerSelect membuka Explorer dengan path terpilih. Mengembalikan pesan
// error untuk baris status, atau "" jika berhasil.
func explorerSelect(path string) string {
	return runExplorer(`explorer.exe /select,"` + path + `"`)
}

func runExplorer(line string) string {
	// explorer butuh format /select,"path" yang tidak dihasilkan oleh
	// escaping argumen bawaan Go, jadi command line ditulis langsung.
	c := exec.Command("explorer.exe")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	if err := c.Start(); err != nil {
		return "Gagal membuka Explorer: " + err.Error()
	}
	go c.Wait()
	return ""
}

func (m *model) count() int {
	if m.path == "" {
		return len(m.drives)
	}
	return len(m.rows)
}

func (m *model) selectedName() string {
	if m.path == "" {
		if m.cursor < len(m.drives) {
			return m.drives[m.cursor].Root
		}
		return ""
	}
	if m.cursor < len(m.rows) {
		return m.rows[m.cursor].name
	}
	return ""
}

func (m *model) indexOfRow(name string) int {
	return slices.IndexFunc(m.rows, func(r row) bool { return r.name == name })
}

func (m *model) indexOfDrive(root string) int {
	return max(slices.IndexFunc(m.drives, func(d drives.Drive) bool { return d.Root == root }), 0)
}

// clamp menjaga kursor tetap valid dan terlihat di layar.
func (m *model) clamp() {
	n := m.count()
	m.cursor = min(max(m.cursor, 0), max(n-1, 0))
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = min(max(m.offset, 0), max(n-h, 0))
}

func errText(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "akses ditolak"
	}
	return err.Error()
}
