package ui

import (
	"fmt"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/analyze"
	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

// maxExpandRows adalah jumlah isi folder yang ditampilkan saat dibuka di chat.
const maxExpandRows = 8

// expansion adalah isi folder yang dibuka di dalam jawaban AI.
type expansion struct {
	rows    []row
	total   int64
	pending int
	loaded  bool
	err     string
}

type expMsg struct {
	path string
	rows []row
	err  error
}

// stop adalah satu posisi kursor di jawaban AI: folder/file yang disebut AI,
// atau isi folder yang sedang dibuka.
type stop struct {
	key     string // identitas yang stabil walau urutan berubah
	path    string
	isDir   bool
	size    int64
	line    int
	parent  int // indeks stop induk; -1 untuk penyebutan AI
	mention bool
}

func loadExp(path string) tea.Cmd {
	return func() tea.Msg {
		rows, err := readDir(path)
		return expMsg{path: path, rows: rows, err: err}
	}
}

func (m *model) updateExp(msg expMsg) tea.Cmd {
	an := m.lastAn
	if an == nil {
		return nil
	}
	e := an.exp[msg.path]
	if e == nil {
		return nil
	}
	e.loaded = true
	if msg.err != nil {
		e.err = errText(msg.err)
		return nil
	}
	e.rows, e.err = msg.rows, ""
	m.refreshExp()
	return m.startTick()
}

// refreshExp mengisi ukuran isi folder yang terbuka dari sizer.
func (m *model) refreshExp() {
	if m.lastAn == nil {
		return
	}
	for _, e := range m.lastAn.exp {
		if e.loaded {
			e.total, e.pending = fillSizes(m.sz, e.rows)
			sortRows(e.rows, false)
		}
	}
}

func (m *model) expPending() int {
	n := 0
	if m.lastAn != nil {
		for _, e := range m.lastAn.exp {
			n += e.pending
		}
	}
	return n
}

// reloadExp memuat ulang semua folder yang terbuka (misalnya setelah hapus).
func (m *model) reloadExp() tea.Cmd {
	if m.lastAn == nil {
		return nil
	}
	var cmds []tea.Cmd
	for p := range m.lastAn.exp {
		cmds = append(cmds, loadExp(p))
	}
	return tea.Batch(cmds...)
}

// toggleExp membuka atau menutup isi folder path di dalam chat.
func (m *model) toggleExp(path string) tea.Cmd {
	an := m.an
	if an.exp[path] != nil {
		delete(an.exp, path)
		return nil
	}
	an.exp[path] = &expansion{}
	return loadExp(path)
}

// emitTree menulis isi folder yang terbuka di bawah stop induknya, termasuk
// subfolder yang ikut dibuka.
// indent adalah awalan baris untuk tingkat ini, termasuk garis │ tingkat atas.
func (m *model) emitTree(an *analysis, lines *[]string, stops *[]stop, parent int, path, indent string, w int) {
	e := an.exp[path]
	if e == nil {
		return
	}
	switch {
	case !e.loaded:
		*lines = append(*lines, indent+dimStyle.Render("memuat…"))
		return
	case e.err != "":
		*lines = append(*lines, indent+warnStyle.Render("tidak bisa dibuka: "+e.err))
		return
	case len(e.rows) == 0:
		*lines = append(*lines, indent+dimStyle.Render("(folder kosong)"))
		return
	}

	shown := e.rows[:min(len(e.rows), maxExpandRows)]
	var restN int
	var restSize int64
	for _, r := range e.rows[len(shown):] {
		restN++
		restSize += r.size
	}
	for i, r := range shown {
		branch, below := "├ ", "│  "
		if i == len(shown)-1 && restN == 0 {
			branch, below = "└ ", "   "
		}
		idx := len(*stops)
		key := "c:" + r.path
		*stops = append(*stops, stop{key: key, path: r.path, isDir: r.isDir && !r.link, size: r.size,
			line: len(*lines), parent: parent})
		text := m.childChip(an, r, key == an.curKey, w-runewidth.StringWidth(indent)-2)
		*lines = append(*lines, dimStyle.Render(indent+branch)+text)
		if r.isDir {
			m.emitTree(an, lines, stops, idx, r.path, indent+below, w)
		}
	}
	if restN > 0 {
		*lines = append(*lines, dimStyle.Render(indent+fmt.Sprintf("└ … %s item lainnya · %s", format.Int(int64(restN)), format.Bytes(restSize))))
	}
}

// childChip menyusun tampilan satu isi folder: ▸/▾ untuk folder, [nama · ukuran].
func (m *model) childChip(an *analysis, r row, selected bool, maxW int) string {
	name := r.name
	marker := ""
	if r.isDir && !r.link {
		name += string(filepath.Separator)
		marker = "▸ "
		if an.exp[r.path] != nil {
			marker = "▾ "
		}
	}
	tail := " · " + sizeText(r)
	if an.gone[r.path] {
		tail = " · terhapus"
	}
	prefix := "["
	if an.marks[r.path] {
		prefix = "[✓ "
	}
	if room := maxW - runewidth.StringWidth(marker+prefix+tail+"]"); runewidth.StringWidth(name) > room {
		name = runewidth.Truncate(name, max(room, 8), "…")
	}
	plain := prefix + name + tail + "]"

	var style lipgloss.Style
	switch {
	case selected:
		style = chipSel
	case an.gone[r.path]:
		style = dimStyle
	case r.isDir:
		style = dirStyle
	default:
		style = lipgloss.NewStyle()
	}
	if r.state != sizer.Done && !selected {
		style = style.Faint(true)
	}
	return dimStyle.Render(marker) + style.Render(plain)
}

// mentionStop membuat stop untuk penyebutan AI ke-n.
func mentionStop(n int, mn analyze.Mention, line int) stop {
	return stop{key: fmt.Sprintf("m:%d", n), path: mn.Path, isDir: mn.IsDir, size: mn.Size, line: line, parent: -1, mention: true}
}
