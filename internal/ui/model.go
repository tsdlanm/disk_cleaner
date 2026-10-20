// Package ui adalah TUI file explorer yang menampilkan ukuran folder.
package ui

import (
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

	"github.com/tsdlanm/cli_app/internal/drives"
	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/sizer"
	"github.com/tsdlanm/cli_app/internal/trash"
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
}

// popup menampilkan informasi (analisis AI) atau konfirmasi hapus.
type popup struct {
	title   string
	lines   []string
	confirm []string // path yang dihapus jika dikonfirmasi; nil untuk popup info
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
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		return Result{}, err
	}
	return Result{Elevate: m.elevate, Path: m.path}, nil
}

func newModel(sz *sizer.Sizer) *model {
	return &model{sz: sz, remember: make(map[string]string), marks: make(map[string]bool), trash: trash.Move}
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
		entries, err := os.ReadDir(path)
		if err != nil && len(entries) == 0 {
			return dirMsg{path: path, err: err}
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
		return dirMsg{path: path, rows: rows}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case drivesMsg:
		m.drives = msg
		if m.path == "" {
			m.cursor = m.indexOfDrive(m.remember[""])
		}

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

	case deleteMsg:
		for _, p := range msg.paths {
			m.sz.Invalidate(p)
		}
		switch {
		case msg.failed == 0:
			m.status = fmt.Sprintf("%d item dipindahkan ke Recycle Bin", len(msg.paths))
		case msg.err != nil:
			m.status = fmt.Sprintf("%d dari %d item gagal dihapus: %v", msg.failed, len(msg.paths), msg.err)
		default:
			m.status = fmt.Sprintf("%d dari %d item gagal dihapus", msg.failed, len(msg.paths))
		}
		cmd = tea.Batch(loadDrives, loadDir(m.path))

	case tickMsg:
		m.ticking = false
		if m.path != "" {
			m.refresh()
			if m.pending > 0 {
				cmd = m.startTick()
			}
		}

	case tea.KeyMsg:
		cmd = m.handleKey(msg.String())
	}
	m.clamp()
	return m, cmd
}

func (m *model) handleKey(key string) tea.Cmd {
	if m.popup != nil {
		if m.popup.confirm != nil {
			switch key {
			case "ctrl+c":
				return tea.Quit
			case "y", "Y":
				paths := m.popup.confirm
				m.popup = nil
				m.status = fmt.Sprintf("Memindahkan %d item ke Recycle Bin…", len(paths))
				return m.deleteCmd(paths)
			case "n", "N", "esc", "q":
				m.popup = nil
			}
			return nil
		}
		switch key {
		case "ctrl+c":
			return tea.Quit
		case "esc", "enter", "q", "a":
			m.popup = nil
		}
		return nil
	}
	m.status = ""
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
	case "backspace", "left", "h", "esc":
		return m.up()
	case "s":
		if m.path != "" {
			m.byName = !m.byName
			m.refresh()
		}
	case "r":
		if m.path == "" {
			return loadDrives
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
		m.popup = m.analysisPopup()
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
	targets := m.deleteTargets()
	if len(targets) == 0 {
		return nil
	}

	const maxList = 8
	var lines []string
	var total int64
	paths := make([]string, len(targets))
	for i, r := range targets {
		paths[i] = r.path
		total += r.size
		if i < maxList {
			name := r.name
			if r.isDir {
				name += string(filepath.Separator)
			}
			name = runewidth.FillRight(runewidth.Truncate(name, 32, "…"), 32)
			lines = append(lines, fmt.Sprintf("  %s %12s", name, sizeText(r)))
		}
	}
	if len(targets) > maxList {
		lines = append(lines, fmt.Sprintf("  … dan %d item lainnya", len(targets)-maxList))
	}
	lines = append(lines, "", "Total   : "+format.Bytes(total))
	if d := m.driveOf(m.path); d >= 0 && m.drives[d].Kind != "Lokal" {
		lines = append(lines, "", "Peringatan: drive "+m.drives[d].Kind+" biasanya tidak punya Recycle Bin;",
			"Windows akan meminta konfirmasi untuk menghapus permanen.")
	}
	lines = append(lines, "", "Ruang drive baru bertambah setelah Recycle Bin dikosongkan.")

	return &popup{
		title:   fmt.Sprintf("Pindahkan %d item ke Recycle Bin?", len(targets)),
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

// analysisPopup menyusun popup analisis AI untuk item yang dipilih.
// Analisisnya sendiri belum tersedia; popup baru menampilkan ringkasan item.
func (m *model) analysisPopup() *popup {
	var lines []string
	switch {
	case m.path == "" && m.cursor < len(m.drives):
		d := m.drives[m.cursor]
		lines = []string{
			"Drive   : " + d.Root + " " + d.Label,
			"Jenis   : " + d.Kind,
		}
		if d.Ready {
			lines = append(lines,
				"Terpakai: "+format.Bytes(int64(d.Used()))+" dari "+format.Bytes(int64(d.Total)),
				"Bebas   : "+format.Bytes(int64(d.Free)))
		}
	case m.path != "" && m.cursor < len(m.rows):
		r := m.rows[m.cursor]
		kind := "File"
		if r.isDir {
			kind = "Folder"
		}
		if r.link {
			kind += " (link)"
		}
		lines = []string{
			"Nama    : " + r.name,
			"Lokasi  : " + r.path,
			"Jenis   : " + kind,
			"Ukuran  : " + sizeText(r),
		}
		if m.total > 0 && !r.denied && !r.link {
			lines[3] += fmt.Sprintf(" (%.1f%% dari folder ini)", float64(r.size)/float64(m.total)*100)
		}
		if r.isDir && !r.link {
			if inf, st := m.sz.Get(r.path); st == sizer.Done {
				lines = append(lines, "Isi     : "+format.Int(inf.Files)+" file · "+format.Int(inf.Dirs)+" folder")
			}
		}
	default:
		return nil
	}
	lines = append(lines, "",
		"Analisis dengan AI belum tersedia.",
		"Nantinya AI akan menjelaskan isi item ini",
		"dan apakah aman untuk dihapus.")
	return &popup{title: "Analisis AI", lines: lines}
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

// refresh mengisi ukuran subfolder dari sizer, menjadwalkan yang belum
// dihitung, lalu mengurutkan ulang. Jika follow, kursor tetap di item yang sama.
func (m *model) refresh() {
	sel := m.selectedName()
	var request []string
	m.total, m.pending = 0, 0
	for i := range m.rows {
		r := &m.rows[i]
		if r.isDir && !r.link {
			inf, st := m.sz.Get(r.path)
			r.size, r.state, r.denied = inf.Size, st, inf.Denied
			if st == sizer.Unknown {
				request = append(request, r.path)
			}
			if st != sizer.Done {
				m.pending++
			}
		}
		m.total += r.size
	}
	m.sortRows()
	if len(request) > 0 {
		m.sz.Request(request...)
	}
	if !m.follow {
		return
	}
	if i := m.indexOfRow(sel); i >= 0 {
		m.cursor = i
	}
}

func (m *model) sortRows() {
	slices.SortStableFunc(m.rows, func(a, b row) int {
		if m.byName {
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
	if m.ticking || m.pending == 0 {
		return nil
	}
	m.ticking = true
	return tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) openExplorer() {
	var line string
	switch {
	case m.path == "" && m.cursor < len(m.drives):
		line = `explorer.exe "` + m.drives[m.cursor].Root + `"`
	case m.path != "" && m.cursor < len(m.rows):
		line = `explorer.exe /select,"` + m.rows[m.cursor].path + `"`
	default:
		return
	}
	// explorer butuh format /select,"path" yang tidak dihasilkan oleh
	// escaping argumen bawaan Go, jadi command line ditulis langsung.
	c := exec.Command("explorer.exe")
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	if err := c.Start(); err != nil {
		m.status = "Gagal membuka Explorer: " + err.Error()
		return
	}
	go c.Wait()
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
