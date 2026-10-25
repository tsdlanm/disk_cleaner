package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

const (
	barWidth     = 10
	headerLines  = 3 // judul, ringkasan, baris kosong
	footerLines  = 2 // status, bantuan tombol
	maxNameWidth = 50
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	dirStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	barStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	selStyle    = lipgloss.NewStyle().Reverse(true)
	markStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	newStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	dangerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	popupStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(0, 2)
)

const (
	keysDrives = "↑↓ pilih · enter buka · tab folder dengan file baru · a analisis AI · r pindai ulang · o Explorer · q keluar"
	keysRecent = "↑↓ pilih · enter buka folder · a analisis AI · o Explorer · tab/esc kembali ke drive · q keluar"
	keysDir    = "↑↓ pilih · enter buka · ← folder induk · esc halaman utama · space tandai · d hapus · a analisis AI · s urut · r refresh · o Explorer · q keluar"
)

func (m *model) listHeight() int {
	h := m.height
	if h == 0 {
		h = 24
	}
	return max(h-headerLines-footerLines, 1)
}

func (m *model) View() string {
	var b strings.Builder
	switch {
	case m.an != nil:
		m.viewAnalysis(&b)

	case m.path == "":
		m.viewDrives(&b)
	default:
		m.viewDir(&b)
	}
	switch {
	case m.settings != nil:
		return m.overlay(b.String(), m.renderPopup(m.settingsPopup()))
	case m.popup != nil:
		return m.overlay(b.String(), m.renderPopup(m.popup))
	}
	return b.String()
}

// footerKeys menambahkan tombol yang hanya berlaku pada kondisi tertentu.
func (m *model) footerKeys(keys string) string {
	keys += " · p pengaturan AI"
	if m.lastAn != nil {
		keys += " · b hasil AI"
	}
	if !m.admin {
		keys += " · A admin"
	}
	return keys
}

func (m *model) viewDrives(b *strings.Builder) {
	m.line(b, titleStyle.Render("Drive"))
	mode := "Administrator"
	if !m.admin {
		mode = "tanpa hak Administrator: folder terkunci tidak ikut terhitung, tekan A untuk restart sebagai Administrator"
	}
	m.line(b, dimStyle.Render(fmt.Sprintf("%d drive · %s", len(m.drives), mode)))
	b.WriteString("\n")

	h := m.listHeight()
	used := 0
	for i := m.offset; i < min(m.offset+h, len(m.drives)); i++ {
		used++
		d := m.drives[i]
		name := d.Root + "  " + runewidth.FillRight(runewidth.Truncate(d.Label, 16, "…"), 16)
		if i == m.cursor && !m.focusRecent {
			name = selStyle.Render(name)
		} else {
			name = dirStyle.Render(name)
		}
		kind := dimStyle.Render(runewidth.FillRight(d.Kind, 8))
		if !d.Ready {
			m.line(b, fmt.Sprintf(" %s  %s  %s", name, kind, dimStyle.Render("tidak siap")))
			continue
		}
		frac := 0.0
		if d.Total > 0 {
			frac = float64(d.Used()) / float64(d.Total)
		}
		usage := fmt.Sprintf("%10s terpakai / %-10s", format.Bytes(int64(d.Used())), format.Bytes(int64(d.Total)))
		free := dimStyle.Render(format.Bytes(int64(d.Free)) + " bebas")

		m.line(b, fmt.Sprintf(" %s  %s  %s  %4.0f%%  %s  %s", name, kind, usage, frac*100, barStyle.Render(bar(frac)), free))
	}

	// Bagian "File baru" mengisi ruang kosong di bawah daftar drive.
	if h-used > 4 {
		b.WriteString("\n")
		used++
		used += m.viewRecent(b, h-used)
	}
	for range h - used {
		b.WriteString("\n")
	}
	if m.focusRecent {
		m.footer(b, m.footerKeys(keysRecent))
	} else {
		m.footer(b, m.footerKeys(keysDrives))
	}
}

func (m *model) viewDir(b *strings.Builder) {
	m.line(b, titleStyle.Render(m.path))

	summary := fmt.Sprintf("%s · %s item", format.Bytes(m.total), format.Int(int64(len(m.rows))))
	if n := m.newFiles().dirs[filepath.Clean(m.path)]; n > 0 {
		summary += fmt.Sprintf(" · %s file baru di sini (%d hari)", format.Int(int64(n)), m.days())
	}
	if m.pending > 0 {
		summary += fmt.Sprintf(" · menghitung %d folder…", m.pending)
	}
	if d := m.driveOf(m.path); d >= 0 {
		summary += fmt.Sprintf(" · drive %s %s bebas", m.drives[d].Root, format.Bytes(int64(m.drives[d].Free)))
	}
	sort := "ukuran"
	if m.byName {
		sort = "nama"
	}
	summary += " · urut " + sort
	m.line(b, dimStyle.Render(summary))
	b.WriteString("\n")

	if len(m.rows) == 0 {
		m.line(b, dimStyle.Render(" (folder kosong)"))
		m.pad(b, 1)
		m.footer(b, keysDir)
		return
	}

	w := m.width
	if w == 0 {
		w = 80
	}
	// Kolom tetap: spasi awal + ukuran + persen + bar + pemisah + penanda file baru.
	nameW := min(max(w-2-11-2-6-2-barWidth-2-22, 10), maxNameWidth)

	h := m.listHeight()
	for i := m.offset; i < min(m.offset+h, len(m.rows)); i++ {
		r := m.rows[i]
		name := r.name
		if r.isDir {
			name += string(filepath.Separator)
		}
		name = runewidth.FillRight(runewidth.Truncate(name, nameW, "…"), nameW)

		size, pct, br := sizeText(r), "", ""
		if !r.denied && !r.link {
			frac := 0.0
			if m.total > 0 {
				frac = float64(r.size) / float64(m.total)
			}
			pct = fmt.Sprintf("%5.1f%%", frac*100)
			br = bar(frac)
		}
		size = fmt.Sprintf("%11s", size)
		pct = fmt.Sprintf("%6s", pct)

		// Penanda file baru (dari "File baru"), atau jumlah file baru di dalam folder.
		tag := ""
		created, isNew := m.newFiles().files[r.path]
		switch {
		case isNew:
			tag = newStyle.Render("● baru · " + relTime(created))
		case r.isDir:
			if n := m.newFiles().dirs[r.path]; n > 0 {
				tag = newStyle.Render(fmt.Sprintf("%s file baru", format.Int(int64(n))))
			}
		}
		switch {
		case i == m.cursor:
			name = selStyle.Render(name)
		case isNew:
			name = newStyle.Render(name)
		case r.isDir:
			name = dirStyle.Render(name)
		}
		switch {
		case r.denied:
			size = warnStyle.Render(size)
		case r.link || r.state != sizer.Done:
			size = dimStyle.Render(size)
		}
		mark := "  "
		if m.marks[r.path] {
			mark = markStyle.Render("✓") + " "
		}
		m.line(b, fmt.Sprintf("%s%s  %s  %s  %s  %s", mark, name, size, pct, barStyle.Render(br), tag))
	}
	m.pad(b, len(m.rows))
	m.footer(b, m.footerKeys(keysDir))
}

func sizeText(r row) string {
	switch {
	case r.denied:
		return "terkunci"
	case r.link:
		return "link"
	case r.state != sizer.Done && r.size == 0:
		return "…"
	case r.state != sizer.Done:
		return format.Bytes(r.size) + "…"
	}
	return format.Bytes(r.size)
}

func bar(frac float64) string {
	n := min(max(int(frac*barWidth+0.5), 0), barWidth)
	return strings.Repeat("█", n) + strings.Repeat("░", barWidth-n)
}

// driveOf mengembalikan indeks drive tempat path berada, atau -1.
func (m *model) driveOf(path string) int {
	root := strings.ToUpper(filepath.VolumeName(path)) + `\`
	for i, d := range m.drives {
		if d.Root == root {
			return i
		}
	}
	return -1
}

// line menulis satu baris yang dipotong sesuai lebar terminal.
func (m *model) line(b *strings.Builder, s string) {
	if m.width > 0 && lipgloss.Width(s) > m.width {
		s = truncateANSI(s, m.width)
	}
	b.WriteString(s)
	b.WriteString("\n")
}

// pad mengisi sisa area daftar dengan baris kosong agar footer tetap di bawah.
func (m *model) pad(b *strings.Builder, n int) {
	for range m.listHeight() - min(n-m.offset, m.listHeight()) {
		b.WriteString("\n")
	}
}

func (m *model) renderPopup(p *popup) string {
	maxW := max(m.screenWidth()-8, 20)
	var b strings.Builder
	style, hint := titleStyle, "esc tutup"
	if p.confirm != nil {
		style, hint = dangerStyle, "y hapus · n/esc batal"
	}
	if p.hint != "" {
		hint = p.hint
	}
	b.WriteString(style.Render(p.title))
	b.WriteString("\n\n")
	for _, l := range p.lines {
		b.WriteString(runewidth.Truncate(l, maxW, "…"))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(hint))
	return popupStyle.Render(b.String())
}

// overlay menumpuk box di tengah layar base.
func (m *model) overlay(base, box string) string {
	w, h := m.screenWidth(), m.listHeight()+headerLines+footerLines
	lines := strings.Split(base, "\n")
	for len(lines) < h {
		lines = append(lines, "")
	}
	boxLines := strings.Split(box, "\n")
	bw := lipgloss.Width(box)
	x := max((w-bw)/2, 0)
	y := max((h-len(boxLines))/2, 0)
	for i, bl := range boxLines {
		if y+i >= len(lines) {
			break
		}
		line := lines[y+i]
		left := ansi.Truncate(line, x, "")
		if lw := ansi.StringWidth(left); lw < x {
			left += strings.Repeat(" ", x-lw)
		}
		right := ansi.TruncateLeft(line, x+bw, "")
		lines[y+i] = left + "\x1b[0m" + bl + "\x1b[0m" + right
	}
	return strings.Join(lines, "\n")
}

func (m *model) screenWidth() int {
	if m.width == 0 {
		return 80
	}
	return m.width
}

func truncateANSI(s string, w int) string {
	return ansi.Truncate(s, w, "…")
}

func (m *model) footer(b *strings.Builder, keys string) {
	status := warnStyle.Render(m.status)
	if n, size := m.marked(); m.status == "" && n > 0 {
		status = markStyle.Render(fmt.Sprintf("%d ditandai · %s · d hapus · space lepas tanda", n, format.Bytes(size)))
	}
	m.line(b, status)
	s := dimStyle.Render(keys)
	if m.width > 0 && lipgloss.Width(s) > m.width {
		s = truncateANSI(s, m.width)
	}
	b.WriteString(s)
}
