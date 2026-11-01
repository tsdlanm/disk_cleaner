package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/bin"
	"github.com/tsdlanm/cli_app/internal/format"
)

type (
	binMsg struct {
		items []bin.Item
		err   error
	}
	// binOpMsg adalah hasil memulihkan atau menghapus permanen item Recycle Bin.
	binOpMsg struct {
		done   string // "dipulihkan" / "dihapus permanen"
		n      int    // jumlah item yang berhasil
		failed int
		err    error // error pertama
	}
)

// binRoots adalah drive lokal yang siap, tempat Recycle Bin dibaca.
func (m *model) binRoots() []string {
	var out []string
	for _, d := range m.drives {
		if d.Ready && d.Kind == "Lokal" {
			out = append(out, d.Root)
		}
	}
	return out
}

// loadBin membaca isi Recycle Bin di background.
func (m *model) loadBin() tea.Cmd {
	list, roots := m.binList, m.binRoots()
	return func() tea.Msg {
		items, err := list(roots)
		return binMsg{items: items, err: err}
	}
}

func (m *model) updateBin(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case binMsg:
		sel, onButton := m.selectedBin(), m.bcursor < 0 && len(m.binItems) > 0
		m.binItems, m.binErr, m.binLoaded = msg.items, msg.err, true
		for p := range m.bmarks {
			if !m.binHas(p) {
				delete(m.bmarks, p)
			}
		}
		m.bcursor = 0
		for i, it := range m.binItems {
			if it.Info == sel {
				m.bcursor = i
			}
		}
		if onButton && len(m.binItems) > 0 {
			m.bcursor = binButton // kursor tetap di tombol "Kosongkan"
		}
	case binOpMsg:
		switch {
		case msg.failed == 0:
			m.status = fmt.Sprintf("%d item %s", msg.n, msg.done)
		default:
			m.status = fmt.Sprintf("%d item %s, %d gagal: %v", msg.n, msg.done, msg.failed, msg.err)
		}
		return tea.Batch(m.loadBin(), loadDrives)
	}
	return nil
}

func (m *model) binHas(info string) bool {
	for _, it := range m.binItems {
		if it.Info == info {
			return true
		}
	}
	return false
}

// binButton adalah posisi kursor di tombol "Kosongkan Recycle Bin", baris
// di atas item pertama.
const binButton = -1

// selectedBin mengembalikan file $I item di kursor ("" jika di tombol).
func (m *model) selectedBin() string {
	if m.bcursor < 0 || m.bcursor >= len(m.binItems) {
		return ""
	}
	return m.binItems[m.bcursor].Info
}

// binTargets mengembalikan item yang ditandai, atau item di kursor.
func (m *model) binTargets() []bin.Item {
	var out []bin.Item
	for _, it := range m.binItems {
		if m.bmarks[it.Info] {
			out = append(out, it)
		}
	}
	if len(out) == 0 && m.bcursor >= 0 && m.bcursor < len(m.binItems) {
		out = append(out, m.binItems[m.bcursor])
	}
	return out
}

// confirmEmptyBin meminta konfirmasi lalu mengosongkan seluruh Recycle Bin.
func (m *model) confirmEmptyBin() {
	n := len(m.binItems)
	if n == 0 {
		m.status = "Recycle Bin sudah kosong"
		return
	}
	var size int64
	for _, it := range m.binItems {
		size += it.Size
	}
	empty := m.binEmpty
	m.popup = &popup{
		title: "Kosongkan Recycle Bin?",
		lines: []string{
			fmt.Sprintf("%s item · %s dihapus permanen dari semua drive.", format.Int(int64(n)), format.Bytes(size)),
			"", "Tidak bisa dibatalkan.",
		},
		hint: "y kosongkan · n/esc batal",
		onYes: func() tea.Cmd {
			clear(m.bmarks)
			m.status = "Mengosongkan Recycle Bin…"
			return func() tea.Msg {
				if err := empty(); err != nil {
					return binOpMsg{done: "dihapus permanen", failed: n, err: err}
				}
				return binOpMsg{done: "dihapus permanen", n: n}
			}
		},
	}
}

// binOp menjalankan op untuk setiap item di background.
func binOp(items []bin.Item, op func(bin.Item) error, done string) tea.Cmd {
	return func() tea.Msg {
		msg := binOpMsg{done: done}
		for _, it := range items {
			if err := op(it); err != nil {
				msg.failed++
				if msg.err == nil {
					msg.err = err
				}
				continue
			}
			msg.n++
		}
		return msg
	}
}

// handleBinKey menangani tombol di halaman Recycle Bin. handled false
// berarti tombol diteruskan ke penanganan biasa.
func (m *model) handleBinKey(key string) (cmd tea.Cmd, handled bool) {
	n := len(m.binItems)
	switch key {
	case "esc":
		m.page = pageDrives
		return nil, true
	case "r":
		return m.loadBin(), true
	case "o":
		m.status = runExplorer(`explorer.exe shell:RecycleBinFolder`)
		return nil, true
	case "E":
		m.confirmEmptyBin()
		return nil, true
	}
	if n == 0 {
		switch key {
		case "up", "k", "down", "j", "pgup", "pgdown", "home", "g", "end", "G", " ", "space", "u", "d", "delete", "enter":
			return nil, true
		}
		return nil, false
	}
	m.bcursor = min(max(m.bcursor, binButton), n-1)
	// Posisi 0 adalah tombol "Kosongkan", posisi 1.. adalah item.
	pos, total, h := m.bcursor+1, n+1, m.listHeight()
	switch key {
	case "up", "k":
		m.bcursor = (pos-1+total)%total - 1
	case "down", "j":
		m.bcursor = (pos+1)%total - 1
	case "pgup":
		m.bcursor = max(pos-h, 0) - 1
	case "pgdown":
		m.bcursor = min(pos+h, total-1) - 1
	case "home", "g":
		m.bcursor = binButton
	case "end", "G":
		m.bcursor = n - 1
	case "enter", "d", "delete":
		if m.bcursor == binButton {
			m.confirmEmptyBin()
			return nil, true
		}
		if key == "enter" {
			return m.restoreBin(), true
		}
		m.confirmPurgeBin()
	case "u":
		if m.bcursor == binButton {
			return nil, true
		}
		return m.restoreBin(), true
	case " ", "space":
		if m.bcursor == binButton {
			return nil, true
		}
		p := m.binItems[m.bcursor].Info
		if m.bmarks[p] {
			delete(m.bmarks, p)
		} else {
			m.bmarks[p] = true
		}
		m.bcursor = min(m.bcursor+1, n-1)
	default:
		return nil, false
	}
	return nil, true
}

// restoreBin memulihkan item yang ditandai (atau di kursor) ke lokasi asalnya.
func (m *model) restoreBin() tea.Cmd {
	items := m.binTargets()
	clear(m.bmarks)
	m.status = fmt.Sprintf("Memulihkan %d item…", len(items))
	return binOp(items, m.binRestore, "dipulihkan ke lokasi asalnya")
}

// confirmPurgeBin meminta konfirmasi lalu menghapus permanen item yang
// ditandai (atau di kursor).
func (m *model) confirmPurgeBin() {
	items := m.binTargets()
	var lines []string
	var size int64
	for i, it := range items {
		size += it.Size
		if i < 8 {
			lines = append(lines, fmt.Sprintf("  %s  %s", truncateLeft(it.Original, 48), format.Bytes(it.Size)))
		}
	}
	if len(items) > 8 {
		lines = append(lines, fmt.Sprintf("  … dan %d item lainnya", len(items)-8))
	}
	lines = append(lines, "", "Total : "+format.Bytes(size), "", "Dihapus permanen; tidak bisa dipulihkan lagi.")
	purge := m.binPurge
	m.popup = &popup{
		title: fmt.Sprintf("Hapus permanen %d item?", len(items)),
		lines: lines,
		hint:  "y hapus permanen · n/esc batal",
		onYes: func() tea.Cmd {
			clear(m.bmarks)
			m.status = fmt.Sprintf("Menghapus permanen %d item…", len(items))
			return binOp(items, purge, "dihapus permanen")
		},
	}
}

// binSummary adalah baris ringkasan halaman Recycle Bin.
func (m *model) binSummary() string {
	switch {
	case !m.binLoaded:
		return dimStyle.Render("membaca Recycle Bin…")
	case m.binErr != nil:
		return warnStyle.Render("Recycle Bin tidak terbaca: " + m.binErr.Error())
	}
	var size int64
	for _, it := range m.binItems {
		size += it.Size
	}
	s := fmt.Sprintf("%s item · %s · dari yang terbesar · ruang ini baru bebas setelah dihapus permanen",
		format.Int(int64(len(m.binItems))), format.Bytes(size))
	if n, msize := m.binMarked(); n > 0 {
		return markStyle.Render(fmt.Sprintf("%d ditandai · %s", n, format.Bytes(msize))) + dimStyle.Render(" · "+s)
	}
	return dimStyle.Render(s)
}

func (m *model) binMarked() (n int, size int64) {
	for _, it := range m.binItems {
		if m.bmarks[it.Info] {
			n++
			size += it.Size
		}
	}
	return n, size
}

// viewBinList menulis isi Recycle Bin dalam paling banyak h baris.
func (m *model) viewBinList(b *strings.Builder, h int) int {
	n := len(m.binItems)
	if n == 0 {
		if m.binLoaded && m.binErr == nil {
			m.line(b, dimStyle.Render(" Recycle Bin kosong."))
			return 1
		}
		return 0
	}
	m.bcursor = min(max(m.bcursor, binButton), n-1)

	// Baris pertama: tombol yang menghapus seluruh isi Recycle Bin sekaligus.
	var total int64
	for _, it := range m.binItems {
		total += it.Size
	}
	button := fmt.Sprintf(" ✕ Kosongkan Recycle Bin · %s item · %s", format.Int(int64(n)), format.Bytes(total))
	if m.bcursor == binButton {
		button = selStyle.Render(button)
	} else {
		button = dangerStyle.Render(button)
	}
	m.line(b, button)
	h--

	if m.bcursor >= 0 {
		if m.bcursor < m.boffset {
			m.boffset = m.bcursor
		}
		if m.bcursor >= m.boffset+h {
			m.boffset = m.bcursor - h + 1
		}
	}
	m.boffset = min(max(m.boffset, 0), max(n-h, 0))

	// Daftar hanya memuat nama; path lengkap item di kursor ada di baris atas
	// (lihat binTopLine), karena path penuh tidak muat.
	nameW := min(max(m.screenWidth()-2-2-recentSizeW-2-len("dihapus ")-recentTimeW-1, 10), recentNameW)
	used := 1
	for i := m.boffset; i < min(m.boffset+h, n); i++ {
		it := m.binItems[i]
		mark := "  "
		if m.bmarks[it.Info] {
			mark = markStyle.Render("✓") + " "
		}
		name := it.Name()
		if it.IsDir {
			name += `\`
		}
		name = runewidth.FillRight(truncateMiddle(name, nameW), nameW)
		switch {
		case i == m.bcursor:
			name = selStyle.Render(name)
		case it.IsDir:
			name = dirStyle.Render(name)
		}
		size := fmt.Sprintf("%*s", recentSizeW, format.Bytes(it.Size))
		m.line(b, mark+name+"  "+size+"  "+dimStyle.Render("dihapus "+relTime(it.Deleted)))
		used++
	}
	return used
}

// binTopLine adalah baris di atas daftar: path lengkap item di kursor, atau
// penjelasan tombol "Kosongkan" saat kursor di sana.
func (m *model) binTopLine() string {
	if !m.binLoaded || m.binErr != nil || len(m.binItems) == 0 {
		return ""
	}
	m.bcursor = min(max(m.bcursor, binButton), len(m.binItems)-1)
	if m.bcursor == binButton {
		return dimStyle.Render(" enter: hapus permanen seluruh isi Recycle Bin (tidak bisa dibatalkan)")
	}
	it := m.binItems[m.bcursor]
	path := it.Original
	if it.IsDir {
		path += `\`
	}
	return " " + titleStyle.Render(truncateLeft(path, max(m.screenWidth()-2, 10)))
}
