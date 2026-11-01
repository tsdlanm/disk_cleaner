package ui

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/analyze"
	"github.com/tsdlanm/cli_app/internal/format"
)

var (
	chipDelete  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	chipReview  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	chipKeep    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	chipSel     = lipgloss.NewStyle().Reverse(true).Bold(true)
	accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	userStyle   = lipgloss.NewStyle().Bold(true)
	cursorStyle = lipgloss.NewStyle().Reverse(true)
)

// Tinggi yang dipakai selain percakapan: judul + garis di atas; status +
// kotak ketik (3 baris) + bantuan tombol di bawah.
const (
	anHeaderLines = 2
	anFooterLines = 5
)

// chatHeight adalah tinggi area percakapan.
func (m *model) chatHeight() int {
	h := m.height
	if h == 0 {
		h = 24
	}
	return max(h-anHeaderLines-anFooterLines, 3)
}

var spinnerFrames = []string{"✻", "✽", "✶", "✳"}

// progressLine adalah baris proses di ujung percakapan, atau "" jika tidak
// ada yang sedang berjalan.
func (an *analysis) progressLine() string {
	spin := accentStyle.Render(spinnerFrames[int(time.Now().UnixMilli()/150)%len(spinnerFrames)])
	switch {
	case an.phase == anCollect:
		return spin + " " + accentStyle.Render("Memindai…") + dimStyle.Render(fmt.Sprintf(" %s file · %s · %.0f detik",
			format.Int(an.prog.Files.Load()), format.Bytes(an.prog.Bytes.Load()), time.Since(an.start).Seconds()))
	case an.phase == anWaiting:
		return spin + " " + accentStyle.Render("Menunggu jawaban…") + dimStyle.Render(fmt.Sprintf(" %s · %.0f detik · %s baris dikirim",
			an.model, time.Since(an.start).Seconds(), format.Int(int64(an.snap.Lines))))
	case an.busy && an.cancelled:
		return spin + " " + dimStyle.Render("Membatalkan…")
	case an.busy:
		return spin + " " + accentStyle.Render("Berpikir…") + dimStyle.Render(fmt.Sprintf(" %.0f detik", time.Since(an.qStart).Seconds()))
	}
	return ""
}

// shortName adalah nama item untuk ditampilkan: path relatif terhadap folder
// yang dianalisis, atau nama foldernya sendiri untuk folder itu.
func (an *analysis) shortName(path string) string {
	if sameDir(path, an.root) {
		return filepath.Base(an.root)
	}
	return an.rel(path)
}

func wrapLines(s string, w int) []string {
	return strings.Split(ansi.Wrap(s, max(w, 1), ""), "\n")
}

// chip menyusun tampilan satu penyebutan item: "▸ path · ukuran", folder biru
// dan file biasa seperti di explorer, dengan ▸/▾ untuk folder. Saran AI tidak
// ditandai di sini; kelompok jawaban dan baris status yang menunjukkannya.
// Mengembalikan teks berwarna dan lebar tampilnya.
func (an *analysis) chip(mn analyze.Mention, selected bool, maxW int) (string, int) {
	name := an.shortName(mn.Path)
	marker := ""
	if mn.IsDir {
		name += string(filepath.Separator)
		marker = "▸ "
		if an.exp[mn.Path] != nil {
			marker = "▾ "
		}
	}
	tail := " · " + format.Bytes(mn.Size)
	if an.gone[mn.Path] {
		tail = " · terhapus"
	}
	prefix := ""
	if an.marks[mn.Path] {
		prefix = "✓ "
	}
	if room := maxW - runewidth.StringWidth(marker+prefix+tail); runewidth.StringWidth(name) > room {
		name = truncateLeft(name, max(room, 8))
	}
	plain := prefix + name + tail

	var style lipgloss.Style
	switch {
	case selected:
		style = chipSel
	case an.gone[mn.Path]:
		style = dimStyle
	case mn.IsDir:
		style = dirStyle
	default:
		style = lipgloss.NewStyle()
	}
	return dimStyle.Render(marker) + style.Render(plain), runewidth.StringWidth(marker + plain)
}

// layoutTranscript menyusun seluruh percakapan ke lebar w. stops berisi setiap
// posisi kursor (item yang disebut AI dan isi folder yang dibuka) sesuai
// urutan tampil.
func (m *model) layoutTranscript(an *analysis, w int) (lines []string, stops []stop) {
	for ei, e := range an.entries {
		// Langkah alat berurutan dirapatkan; entri lain dipisah baris kosong.
		if ei > 0 && !(e.kind == entTool && an.entries[ei-1].kind == entTool) {
			lines = append(lines, "")
		}
		switch e.kind {
		case entUser:
			for i, l := range wrapLines(e.text, w-2) {
				prefix := "  "
				if i == 0 {
					prefix = promptStyle.Render("›") + " "
				}
				lines = append(lines, prefix+userStyle.Render(l))
			}
		case entTool:
			label := runewidth.Truncate(e.text, max(w-2, 10), "…")
			lines = append(lines, accentStyle.Render("●")+" "+userStyle.Render(label))
			sum := e.sum
			if !e.done {
				sum = "…"
			}
			lines = append(lines, dimStyle.Render("  ⎿ "+runewidth.Truncate(sum, max(w-4, 10), "…")))
		case entAnswer:
			if e.res == an.res {
				// Analisis pertama: ukuran folder dan bar di atas ringkasan.
				if h := an.summaryHeader(w); len(h) > 0 {
					lines = append(lines, h...)
					lines = append(lines, "")
				}
			}
			m.layoutAnswer(an, ei, e.res, w, &lines, &stops)
		case entError:
			for i, l := range wrapLines(e.text, w-2) {
				prefix := "  "
				if i == 0 {
					prefix = warnStyle.Render("✕") + " "
				}
				lines = append(lines, prefix+warnStyle.Render(l))
			}
		case entNote:
			for _, l := range strings.Split(e.text, "\n") {
				for _, wl := range wrapLines(l, w-2) {
					lines = append(lines, dimStyle.Render("  "+wl))
				}
			}
		}
	}
	if p := an.progressLine(); p != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, p)
	}
	an.syncCursor(stops)
	return lines, stops
}

// maxRowW membatasi lebar baris item di hasil, supaya ukurannya tetap dekat
// dengan namanya di layar lebar.
const maxRowW = 90

// platformRe adalah akhiran platform pada nama ekstensi/paket
// ("-win32-x64") yang tidak penting untuk memutuskan; disembunyikan di daftar.
var platformRe = regexp.MustCompile(`-(win32|linux|darwin|alpine)-(x64|x86|ia32|arm64|armhf)$`)

// groupStyle mengembalikan simbol dan warna judul kelompok saran.
func groupStyle(title string) (string, lipgloss.Style, bool) {
	switch {
	case strings.HasPrefix(title, "Aman dihapus"):
		return "✕", chipDelete, true
	case strings.HasPrefix(title, "Perlu dicek"):
		return "?", chipReview, true
	case strings.HasPrefix(title, "Sebaiknya disimpan"):
		return "●", chipKeep, true
	}
	return "", lipgloss.Style{}, false
}

// percent menuliskan bagian dari total, misalnya "12%" atau "<1%".
func percent(part, total int64) string {
	if total <= 0 {
		return ""
	}
	if p := part * 100 / total; p > 0 {
		return fmt.Sprintf("%d%%", p)
	}
	return "<1%"
}

// summaryHeader adalah bagian paling atas hasil: ukuran folder, bar yang
// menunjukkan berapa bagiannya aman dihapus, perlu dicek, dan disimpan, serta
// legendanya dengan persen.
func (an *analysis) summaryHeader(w int) []string {
	s := an.snap
	if s == nil || s.Size <= 0 || an.res == nil {
		return nil
	}
	var sums = map[analyze.Action]int64{}
	for _, mn := range an.res.Mentions {
		sums[mn.Action] += mn.Size
	}
	rest := s.Size
	for _, a := range []analyze.Action{analyze.ActionDelete, analyze.ActionReview, analyze.ActionKeep} {
		rest -= sums[a]
	}
	rest = max(rest, 0)

	barW := min(max(w-4, 20), 60)
	type seg struct {
		size  int64
		style lipgloss.Style
		char  string
	}
	segs := []seg{
		{sums[analyze.ActionDelete], chipDelete, "█"},
		{sums[analyze.ActionReview], chipReview, "█"},
		{sums[analyze.ActionKeep], chipKeep, "█"},
		{rest, dimStyle, "░"},
	}
	// Tiap bagian yang ada mendapat minimal satu sel; sisanya dibagi menurut ukuran.
	cells := make([]int, len(segs))
	used, largest := 0, 0
	for i, sg := range segs {
		if sg.size <= 0 {
			continue
		}
		cells[i] = max(int(int64(barW)*sg.size/s.Size), 1)
		used += cells[i]
		if cells[i] > cells[largest] {
			largest = i
		}
	}
	cells[largest] += barW - used // pembulatan masuk ke bagian terbesar
	var bar strings.Builder
	for i, sg := range segs {
		if cells[i] > 0 {
			bar.WriteString(sg.style.Render(strings.Repeat(sg.char, cells[i])))
		}
	}

	legend := func(sym string, st lipgloss.Style, name string, size int64) string {
		return st.Render(sym) + dimStyle.Render(fmt.Sprintf(" %s %s (%s)", name, format.Bytes(size), percent(size, s.Size)))
	}
	var parts []string
	for _, l := range []struct {
		a    analyze.Action
		sym  string
		st   lipgloss.Style
		name string
	}{{analyze.ActionDelete, "✕", chipDelete, "aman"}, {analyze.ActionReview, "?", chipReview, "cek"}, {analyze.ActionKeep, "●", chipKeep, "simpan"}} {
		if sums[l.a] > 0 {
			parts = append(parts, legend(l.sym, l.st, l.name, sums[l.a]))
		}
	}
	if rest > 0 {
		parts = append(parts, legend("░", dimStyle, "lainnya", rest))
	}
	title := truncateLeft(an.root, max(w-30, 12))
	return []string{
		"  " + userStyle.Render(title) + dimStyle.Render(" · "+format.Bytes(s.Size)+" · "+format.Int(s.Files)+" file"),
		"  " + bar.String(),
		"  " + strings.Join(parts, dimStyle.Render("  ")),
	}
}

// isGroupTitle melaporkan apakah baris tampilan adalah judul kelompok saran
// ("✕ Aman dihapus · 4 item").
func isGroupTitle(line string) bool {
	s := strings.TrimSpace(ansi.Strip(line))
	return strings.Contains(s, " item") && (strings.HasPrefix(s, "✕ ") || strings.HasPrefix(s, "? ") || strings.HasPrefix(s, "● "))
}

// commonDir adalah folder induk yang sama dari semua names (diakhiri "\"),
// atau "" jika tidak ada.
func commonDir(names []string) string {
	var common []string
	for i, n := range names {
		parts := strings.Split(filepath.Dir(n), `\`)
		if parts[0] == "." {
			return ""
		}
		if i == 0 {
			common = parts
			continue
		}
		k := 0
		for k < len(common) && k < len(parts) && strings.EqualFold(common[k], parts[k]) {
			k++
		}
		common = common[:k]
	}
	if len(common) == 0 {
		return ""
	}
	return strings.Join(common, `\`) + `\`
}

// chipRow menyusun satu baris item di daftar hasil: ▸ nama di kiri (folder
// biru), ukuran rata kanan, selebar w. prefix (folder induk yang sama untuk
// satu kelompok) dan akhiran platform tidak ditulis; path lengkapnya tampil di
// baris status saat item dipilih.
func (an *analysis) chipRow(mn analyze.Mention, selected bool, w int, prefix string) string {
	pct := ""
	if an.snap != nil && an.snap.Size > 0 && !an.gone[mn.Path] {
		pct = percent(mn.Size, an.snap.Size)
	}
	name := an.shortName(mn.Path)
	if prefix != "" && len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
		name = name[len(prefix):]
	}
	name = platformRe.ReplaceAllString(name, "")
	marker := "  "
	if mn.IsDir {
		name += `\`
		marker = "▸ "
		if an.exp[mn.Path] != nil {
			marker = "▾ "
		}
	}
	if an.marks[mn.Path] {
		name = "✓ " + name
	}
	size := format.Bytes(mn.Size)
	if an.gone[mn.Path] {
		size = "terhapus"
	}
	if pct != "" {
		size = fmt.Sprintf("%9s %4s", size, pct)
	}
	room := max(w-2-runewidth.StringWidth(size)-2, 8)
	if runewidth.StringWidth(name) > room {
		name = truncateMiddle(name, room)
	}
	gap := strings.Repeat(" ", max(w-2-runewidth.StringWidth(name)-runewidth.StringWidth(size), 2))

	nameStyle, sizeStyle := lipgloss.NewStyle(), lipgloss.NewStyle()
	switch {
	case selected:
		return dimStyle.Render(marker) + chipSel.Render(name+gap+size)
	case an.gone[mn.Path]:
		nameStyle, sizeStyle = dimStyle, dimStyle
	case mn.IsDir:
		nameStyle = dirStyle
	}
	return dimStyle.Render(marker) + nameStyle.Render(name) + gap + sizeStyle.Render(size)
}

// layoutAnswer membungkus satu jawaban AI ke lebar w, diawali ●. Jawaban
// analisis tampil seperti daftar di explorer:
//
//	✕ Aman dihapus · 4 item
//	  extensions\                       ← folder induk yang sama, sekali saja
//	  ▸ claude-code-2.1.312    296.9 MB
//	    penjelasan (redup)               ← sekali untuk butir berurutan yang sama
//
// Isi folder yang dibuka tampil tepat di bawah nama foldernya. Paragraf lain
// (ringkasan, jawaban chat) dibungkus biasa dan penyebutan item di dalamnya
// tidak dipotong di tengah.
func (m *model) layoutAnswer(an *analysis, ei int, res *analyze.Result, w int, lines *[]string, stops *[]stop) {
	const ind = 2
	first := len(*lines)
	nth := 0
	rowW := min(w, maxRowW) - ind - 2

	// Pecah jawaban menjadi paragraf (per baris teks), tiap paragraf berisi
	// teks dan penyebutan item sesuai urutan.
	type para struct {
		segs    []analyze.Segment
		lead    string
		heading bool
		bullet  bool
		ids     []int  // item yang disebut (yang dikenal)
		expl    string // penjelasan butir
		group   int    // indeks paragraf judul kelompoknya; -1 jika tidak ada
	}
	var paras []para
	cur := para{}
	for _, sg := range res.Segments() {
		if sg.ID > 0 {
			cur.segs = append(cur.segs, sg)
			continue
		}
		for i, part := range strings.Split(sg.Text, "\n") {
			if i > 0 {
				paras = append(paras, cur)
				cur = para{}
			}
			if part != "" {
				cur.segs = append(cur.segs, analyze.Segment{Text: part})
			}
		}
	}
	paras = append(paras, cur)

	group := -1
	for i := range paras {
		p := &paras[i]
		p.group = group
		var text strings.Builder
		for j, sg := range p.segs {
			if sg.ID > 0 {
				if _, ok := res.Mentions[sg.ID]; ok {
					p.ids = append(p.ids, sg.ID)
				}
				text.WriteString(" ")
				continue
			}
			t := sg.Text
			if j == 0 {
				p.lead = strings.TrimSpace(t)
				t = strings.TrimLeft(strings.TrimSpace(t), "•- ")
			}
			text.WriteString(t)
		}
		hasMention := false
		for _, sg := range p.segs {
			hasMention = hasMention || sg.ID > 0
		}
		switch {
		case hasMention && (strings.HasPrefix(p.lead, "•") || strings.HasPrefix(p.lead, "- ")):
			p.bullet = true
			expl := strings.Join(strings.Fields(text.String()), " ")
			expl = strings.NewReplacer(" ,", ",", " .", ".", " ;", ";").Replace(expl)
			p.expl = strings.TrimLeft(expl, ",;: ")
		case !hasMention && strings.HasSuffix(p.lead, ":") && runewidth.StringWidth(p.lead) <= 40:
			p.heading = true
			group = i
			p.group = i
		}
	}
	// Folder induk yang sama untuk tiap kelompok (minimal dua item).
	prefix := make(map[int]string)
	count := make(map[int]int)
	gsize := make(map[int]int64) // total ukuran item per kelompok
	names := make(map[int][]string)
	for _, p := range paras {
		if p.bullet && p.group >= 0 {
			for _, id := range p.ids {
				names[p.group] = append(names[p.group], an.shortName(res.Mentions[id].Path))
				count[p.group]++
				gsize[p.group] += res.Mentions[id].Size
			}
		}
	}
	for g, n := range names {
		if len(n) >= 2 {
			prefix[g] = commonDir(n)
		}
	}

	blank := func() {
		if len(*lines) == first {
			return
		}
		if strings.TrimSpace(ansi.Strip((*lines)[len(*lines)-1])) != "" {
			*lines = append(*lines, "")
		}
	}

	for i, p := range paras {
		switch {
		case len(p.segs) == 0:
			blank()

		case p.heading:
			// Judul kelompok, dipisah dari bagian sebelumnya.
			blank()
			title := strings.TrimSuffix(p.lead, ":")
			line := userStyle.Render(title)
			if sym, st, ok := groupStyle(title); ok {
				info := fmt.Sprintf(" · %d item · %s", count[i], format.Bytes(gsize[i]))
				if an.snap != nil && an.snap.Size > 0 {
					info += " · " + percent(gsize[i], an.snap.Size) + " dari folder"
				}
				line = st.Render(sym) + " " + st.Bold(true).Render(title) + dimStyle.Render(info)
			}
			*lines = append(*lines, strings.Repeat(" ", ind)+line)
			if pre := prefix[i]; pre != "" {
				*lines = append(*lines, strings.Repeat(" ", ind+2)+dimStyle.Render(pre))
			}

		case p.bullet:
			// Satu baris per item (isi folder yang dibuka tepat di bawahnya),
			// lalu penjelasan redup. Penjelasan yang sama dengan butir
			// berikutnya ditulis sekali, di bawah item terakhirnya.
			pre := ""
			if p.group >= 0 {
				pre = prefix[p.group]
			}
			for _, id := range p.ids {
				mn := res.Mentions[id]
				s := mentionStop(fmt.Sprintf("m:%d:%d", ei, nth), mn)
				nth++
				s.line = len(*lines)
				*lines = append(*lines, strings.Repeat(" ", ind+2)+an.chipRow(mn, an.itemsFocus && s.key == an.curKey, rowW, pre))
				*stops = append(*stops, s)
				if mn.IsDir && an.exp[mn.Path] != nil {
					m.emitTree(an, lines, stops, len(*stops)-1, mn.Path, strings.Repeat(" ", ind+4), w)
				}
			}
			if next := i + 1; p.expl != "" && !(next < len(paras) && paras[next].bullet && paras[next].expl == p.expl) {
				for _, l := range wrapLines(p.expl, max(rowW-2, 10)) {
					*lines = append(*lines, strings.Repeat(" ", ind+4)+dimStyle.Render(l))
				}
			}

		default:
			// Paragraf biasa: kata dan penyebutan item mengalir, terbungkus.
			var cur strings.Builder
			cur.WriteString(strings.Repeat(" ", ind))
			curW, lineStart, hang := ind, ind, ind
			pendingSpace, paraStart := false, true
			var opened []int
			newLine := func(indent int) {
				*lines = append(*lines, cur.String())
				cur.Reset()
				cur.WriteString(strings.Repeat(" ", indent))
				curW, lineStart = indent, indent
			}
			place := func(text string, width int) {
				space := 0
				if pendingSpace && curW > lineStart {
					space = 1
				}
				if curW > lineStart && curW+space+width > w {
					newLine(hang)
					space = 0
				}
				if space == 1 {
					cur.WriteString(" ")
				}
				cur.WriteString(text)
				curW += space + width
				pendingSpace = false
			}
			for _, sg := range p.segs {
				if sg.ID > 0 {
					mn, ok := res.Mentions[sg.ID]
					if !ok {
						continue
					}
					s := mentionStop(fmt.Sprintf("m:%d:%d", ei, nth), mn)
					nth++
					text, width := an.chip(mn, an.itemsFocus && s.key == an.curKey, w-hang)
					place(text, width)
					s.line = len(*lines)
					*stops = append(*stops, s)
					if mn.IsDir && an.exp[mn.Path] != nil {
						opened = append(opened, len(*stops)-1)
					}
					paraStart = false
					continue
				}
				for j, word := range strings.Split(sg.Text, " ") {
					if j > 0 {
						pendingSpace = true
					}
					if word == "" {
						continue
					}
					place(word, runewidth.StringWidth(word))
					if paraStart && (word == "•" || word == "-") {
						hang = curW + 1
					}
					paraStart = false
				}
			}
			newLine(ind)
			for _, i := range opened {
				m.emitTree(an, lines, stops, i, (*stops)[i].path, "    ", w)
			}
		}
	}
	if len(*lines) == first {
		*lines = append(*lines, "")
	}
	// Baris kosong di ujung jawaban dibuang; yang berisi item tidak pernah kosong.
	for len(*lines) > first+1 && strings.TrimSpace(ansi.Strip((*lines)[len(*lines)-1])) == "" {
		*lines = (*lines)[:len(*lines)-1]
	}
	(*lines)[first] = accentStyle.Render("●") + " " + strings.TrimPrefix((*lines)[first], strings.Repeat(" ", ind))
}

// titleLine menulis judul di kiri dan keterangan di kanan, selebar layar.
func (m *model) titleLine(b *strings.Builder, left, right string) {
	w := m.screenWidth()
	if gap := w - lipgloss.Width(left) - lipgloss.Width(right) - 1; gap >= 1 {
		m.line(b, left+strings.Repeat(" ", gap)+right)
		return
	}
	m.line(b, left)
}

func (m *model) viewAnalysis(b *strings.Builder) {
	an := m.an
	w := m.screenWidth()

	right := an.model
	if an.sess != nil && an.sess.Usage.Requests > 0 {
		right = fmt.Sprintf("%d pertanyaan · %s token · %s", an.sess.Questions(), format.Int(int64(an.sess.Usage.Total())), an.model)
	}
	m.titleLine(b, titleStyle.Render("Analisis AI")+dimStyle.Render(" · "+truncateLeft(an.root, max(w-lipgloss.Width(right)-22, 12))), dimStyle.Render(right))
	m.line(b, dimStyle.Render(strings.Repeat("─", w)))

	h := m.chatHeight()
	cursorLine := -1
	var lines []string
	var stops []stop
	if an.showData {
		lines = m.dataLines(an)
		if ds := an.dataStops(); len(ds) > 0 {
			cursorLine = ds[min(an.dataCursor, len(ds)-1)]
		}
	} else {
		an.lastW = max(w-2, 20)
		lines, stops = m.layoutTranscript(an, an.lastW)
		if an.itemsFocus && len(stops) > 0 {
			cursorLine = stops[an.cursor].line
		}
	}
	switch {
	case cursorLine >= 0 && !an.free:
		if cursorLine < an.textOff {
			an.textOff = cursorLine
			switch {
			case an.showData:
			case an.cursor == 0:
				an.textOff = 0 // item pertama: tampilkan dari awal, termasuk ringkasan
			default:
				// Item pertama sebuah kelompok: ikutkan judulnya (dan baris
				// folder induk di bawah judul, bila ada).
				for k := cursorLine - 1; k >= max(cursorLine-2, 0); k-- {
					if isGroupTitle(lines[k]) {
						an.textOff = k
						break
					}
				}
			}
		}
		if cursorLine >= an.textOff+h {
			an.textOff = cursorLine - h + 1
		}
	case an.stick && !an.showData:
		an.textOff = len(lines) - h
	}
	an.textOff = min(max(an.textOff, 0), max(len(lines)-h, 0))
	if an.free && an.textOff >= len(lines)-h {
		an.free, an.stick = false, !an.showData // sudah di ujung bawah: ikuti lagi
	}
	for i := range h {
		if j := an.textOff + i; j < len(lines) {
			m.line(b, " "+lines[j])
		} else {
			b.WriteString("\n")
		}
	}

	m.line(b, m.analysisStatus(an, stops, len(lines) > h))
	for _, l := range m.inputBox(an, w) {
		m.line(b, l)
	}
	keys := dimStyle.Render(m.analysisKeys(an))
	if lipgloss.Width(keys) > w {
		keys = truncateANSI(keys, w)
	}
	b.WriteString(keys)
}

// analysisStatus adalah baris di atas kotak ketik: pesan, jumlah tanda,
// detail item terpilih, atau petunjuk perintah.
func (m *model) analysisStatus(an *analysis, stops []stop, scrollable bool) string {
	input := string(an.input)
	switch n, size := an.marked(); {
	case m.status != "":
		return " " + warnStyle.Render(m.status)
	case n > 0:
		return " " + markStyle.Render(fmt.Sprintf("%d ditandai · %s · d hapus · space lepas tanda", n, format.Bytes(size)))
	case an.itemsFocus && an.cursor < len(stops):
		return " " + an.describe(stops[an.cursor], max(m.screenWidth()-2, 20))
	case strings.HasPrefix(input, "/"):
		var names []string
		for _, c := range anCommands {
			if strings.HasPrefix(c.name, strings.Fields(input)[0]) {
				names = append(names, c.name)
			}
		}
		if len(names) == 0 {
			return " " + dimStyle.Render("perintah tidak dikenal · /bantuan")
		}
		return " " + dimStyle.Render(strings.Join(names, " · "))
	case scrollable && !an.stick:
		return " " + dimStyle.Render("pgdn ke bawah")
	}
	return ""
}

// describe menjelaskan satu item terpilih: saran AI beserta alasannya, path, dan ukuran.
func (an *analysis) describe(s stop, w int) string {
	label, style := "", lipgloss.NewStyle()
	switch s.action {
	case analyze.ActionDelete:
		label, style = "✕ kemungkinan aman", chipDelete
	case analyze.ActionReview:
		label, style = "? cek dulu", chipReview
	case analyze.ActionKeep:
		label, style = "● simpan", chipKeep
	}
	path := an.shortName(s.path)
	if s.isDir {
		path += string(filepath.Separator)
	}
	text := path + " · " + format.Bytes(s.size)
	if an.gone[s.path] {
		text = path + " · terhapus"
	}
	if label != "" {
		text = label + " · " + text
	}
	if s.reason != "" {
		text += " — " + s.reason
	}
	text = runewidth.Truncate(text, w, "…")
	if label != "" && strings.HasPrefix(text, label) {
		return style.Render(label) + dimStyle.Render(strings.TrimPrefix(text, label))
	}
	return dimStyle.Render(text)
}

// inputBox menyusun kotak ketik tiga baris di bawah percakapan.
func (m *model) inputBox(an *analysis, w int) []string {
	prompt := promptStyle.Render("›") + " "
	cursor := cursorStyle.Render(" ")
	inner := max(w-6, 10)

	border := accentStyle
	var body string
	switch {
	case an.showData:
		border, body = dimStyle, dimStyle.Render("tampilan data folder · esc kembali ke percakapan")
	case an.phase == anConfirm:
		border, body = accentStyle, dimStyle.Render("enter kirim ke "+an.model+" · v lihat data · esc batal")
	case an.phase == anCollect || an.phase == anWaiting:
		border, body = dimStyle, dimStyle.Render("menunggu analisis selesai · esc batal")
	case an.busy:
		border, body = dimStyle, dimStyle.Render("menunggu jawaban · esc batalkan pertanyaan")
	case an.itemsFocus:
		border, body = dimStyle, dimStyle.Render("memilih item · tab atau esc untuk kembali mengetik")
	case len(an.input) == 0:
		hint := "ketik pertanyaan, atau / untuk perintah"
		if an.phase == anFailed {
			hint = "ketik /ulang, /data, atau /model"
		}
		// Kursor menutupi huruf pertama petunjuk, seperti placeholder biasa.
		r := []rune(runewidth.Truncate(hint, max(inner-3, 4), "…"))
		body = prompt + cursorStyle.Render(string(r[:1])) + dimStyle.Render(string(r[1:]))
	default:
		body = prompt + userStyle.Render(truncateLeft(string(an.input), max(inner-3, 4))) + cursor
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border.GetForeground()).Padding(0, 1).Width(w - 2).Render(body)
	return strings.Split(box, "\n")
}

// analysisKeys adalah bantuan tombol sesuai keadaan layar.
func (m *model) analysisKeys(an *analysis) string {
	switch {
	case an.showData:
		return "↑↓ pilih · enter buka di explorer · space tandai · d hapus · esc kembali ke percakapan"
	case an.phase == anConfirm:
		return "enter kirim ke AI · v lihat data · esc batal · pgup/pgdn gulir"
	case an.phase == anCollect || an.phase == anWaiting:
		return "esc batal · pgup/pgdn gulir"
	case an.busy:
		return "esc batalkan pertanyaan · pgup/pgdn gulir"
	case an.itemsFocus:
		return "↑↓ pilih · enter buka folder · space tandai · d hapus · e explorer · o Explorer · tab/esc kembali"
	}
	return "enter kirim · tab pilih item · / perintah · pgup/pgdn gulir · esc kembali"
}

// dataLines menyusun tampilan data folder: setiap baris item bisa dipilih,
// dengan kolom tanda di kiri untuk ✓ (ditandai) dan baris terhapus diredupkan.
func (m *model) dataLines(an *analysis) []string {
	stops := an.dataStops()
	sel := -1
	if len(stops) > 0 {
		sel = stops[min(an.dataCursor, len(stops)-1)]
	}
	raw := strings.Split(strings.TrimRight(an.snap.Text, "\n"), "\n")
	out := make([]string, len(raw))
	for i, l := range raw {
		it := an.snap.Items[an.snap.LineIDs[i]]
		gutter := "  "
		switch {
		case an.gone[it.Path]:
			l = dimStyle.Render(l + "  · terhapus")
		case i == sel:
			// Sorot dari nomor [id] sampai akhir; indentasi tetap biasa.
			trimmed := strings.TrimLeft(l, " ")
			l = l[:len(l)-len(trimmed)] + selStyle.Render(trimmed)
		}
		if an.marks[it.Path] {
			gutter = markStyle.Render("✓") + " "
		}
		out[i] = gutter + l
	}
	return out
}

