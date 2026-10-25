package ui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/analyze"
	"github.com/tsdlanm/cli_app/internal/format"
)

type anPhase int

const (
	anCollect anPhase = iota // memindai folder
	anWaiting                // menunggu jawaban AI
	anDone
	anFailed
)

// analysis adalah satu analisis AI beserta layar hasilnya.
type analysis struct {
	id       int
	root     string
	model    string
	phase    anPhase
	ctx      context.Context
	cancel   context.CancelFunc
	provider analyze.Provider
	prog     *analyze.Progress
	snap     *analyze.Snapshot
	res      *analyze.Result
	err      error
	start    time.Time
	elapsed  time.Duration

	// Kursor di jawaban: indeks ke daftar stop hasil layoutAnswer. curKey
	// menjaga kursor tetap di item yang sama saat isi folder dibuka/ditutup.
	cursor     int
	curKey     string
	lastW      int                   // lebar teks terakhir, untuk menyusun stop saat tombol ditekan
	exp        map[string]*expansion // folder yang dibuka di dalam jawaban
	dataCursor int                   // posisi kursor di tampilan data (indeks dataStops)
	textOff    int                   // baris teratas yang terlihat
	free       bool                  // pengguna menggulir sendiri; jangan paksa kursor terlihat
	showData   bool                  // tampilkan data yang dikirim ke AI
	marks      map[string]bool
	markItems  map[string]delItem // detail item yang ditandai, untuk konfirmasi hapus
	gone       map[string]bool    // item yang sudah dihapus
}

type (
	snapMsg struct {
		id   int
		snap *analyze.Snapshot
		err  error
	}
	aiMsg struct {
		id  int
		res *analyze.Result
		err error
	}
	anTickMsg struct{ id int }
)

const (
	keysAnalysis = "↑↓ pindah · enter buka/tutup folder · ← ke induk · e ke explorer · space tandai · d hapus · o Explorer · pgup/pgdn gulir · v data folder · r ulang · p pengaturan AI · esc kembali"
	keysData     = "↑↓ pilih item · enter/e buka di explorer · space tandai · d hapus · o Explorer · v kembali ke jawaban · r ulang · p pengaturan AI · esc kembali"
)

var (
	chipDelete = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Underline(true)
	chipReview = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Underline(true)
	chipKeep   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Underline(true)
	chipSel    = lipgloss.NewStyle().Reverse(true).Bold(true)
)

func anTick(id int) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return anTickMsg{id: id} })
}

// analysisTarget menentukan folder yang dianalisis dari posisi kursor:
// drive yang dipilih, folder yang dipilih, atau folder saat ini untuk file.
func (m *model) analysisTarget() string {
	if m.path == "" {
		if m.cursor < len(m.drives) && m.drives[m.cursor].Ready {
			return m.drives[m.cursor].Root
		}
		return ""
	}
	if m.cursor < len(m.rows) {
		if r := m.rows[m.cursor]; r.isDir && !r.link && !r.denied {
			return r.path
		}
	}
	return m.path
}

// requestAnalysis menganalisis item di kursor.
func (m *model) requestAnalysis() tea.Cmd {
	root := m.analysisTarget()
	if root == "" {
		m.status = "Pilih drive atau folder untuk dianalisis"
		return nil
	}
	return m.analyzePath(root)
}

// analyzePath memulai analisis root, atau meminta API key lebih dulu.
func (m *model) analyzePath(root string) tea.Cmd {
	if m.cfg.NeedsKey() {
		m.openSettings(root, true)
		return nil
	}
	return m.startAnalysis(root)
}

func (m *model) startAnalysis(root string) tea.Cmd {
	p, err := m.newProvider(m.cfg)
	if err != nil {
		m.status = "AI belum siap: " + err.Error()
		return nil
	}
	if m.lastAn != nil && m.lastAn.cancel != nil {
		m.lastAn.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	model := m.cfg.ModelName()
	m.anSeq++
	an := &analysis{
		id: m.anSeq, root: root, model: model, phase: anCollect,
		ctx: ctx, cancel: cancel, provider: p, prog: &analyze.Progress{}, start: time.Now(),
		exp: make(map[string]*expansion), marks: make(map[string]bool),
		markItems: make(map[string]delItem), gone: make(map[string]bool),
	}
	m.an, m.lastAn = an, an
	lim := m.cfg.Limits()
	build := func() tea.Msg {
		s, err := analyze.Build(ctx, root, lim, an.prog)
		return snapMsg{id: an.id, snap: s, err: err}
	}
	return tea.Batch(build, anTick(an.id))
}

func (an *analysis) fail(err error) {
	an.phase, an.elapsed = anFailed, time.Since(an.start)
	if errors.Is(err, context.Canceled) {
		err = errors.New("analisis dibatalkan")
	}
	an.err = err
	an.cancel()
}

func (m *model) updateAnalysis(msg tea.Msg) tea.Cmd {
	an := m.lastAn
	switch msg := msg.(type) {
	case snapMsg:
		if an == nil || an.id != msg.id || an.phase != anCollect {
			return nil
		}
		if msg.err != nil {
			an.fail(msg.err)
			return nil
		}
		an.snap, an.phase = msg.snap, anWaiting
		ctx, p, s, id := an.ctx, an.provider, msg.snap, an.id
		return func() tea.Msg {
			res, err := analyze.Analyze(ctx, p, s)
			return aiMsg{id: id, res: res, err: err}
		}

	case aiMsg:
		if an == nil || an.id != msg.id || an.phase != anWaiting {
			return nil
		}
		if msg.err != nil {
			an.fail(msg.err)
		} else {
			an.res, an.phase, an.elapsed = msg.res, anDone, time.Since(an.start)
			an.cursor, an.curKey = 0, "m:0"
			an.cancel()
		}
		if m.an == nil {
			m.status = "Analisis AI selesai, tekan b untuk melihat hasilnya"
		}

	case anTickMsg:
		if an != nil && an.id == msg.id && (an.phase == anCollect || an.phase == anWaiting) {
			return anTick(an.id)
		}
	}
	return nil
}

// dataStops mengembalikan nomor baris data yang berisi item (bisa dipilih).
func (an *analysis) dataStops() []int {
	if an.snap == nil {
		return nil
	}
	var out []int
	for i, id := range an.snap.LineIDs {
		if id > 0 {
			out = append(out, i)
		}
	}
	return out
}

// answerStops menyusun ulang posisi kursor di jawaban AI dan menyelaraskan
// kursor dengan item yang sebelumnya dipilih.
func (m *model) answerStops(an *analysis) []stop {
	if an.res == nil {
		return nil
	}
	w := an.lastW
	if w == 0 {
		w = max(m.screenWidth()-2, 20)
	}
	_, stops := m.layoutAnswer(an, w)
	return stops
}

// syncCursor memindahkan kursor ke stop dengan curKey; jika item itu sudah
// tidak ada (misalnya foldernya ditutup), kursor tetap di posisi terdekat.
func (an *analysis) syncCursor(stops []stop) {
	if len(stops) == 0 {
		an.cursor = 0
		return
	}
	for i, s := range stops {
		if s.key == an.curKey {
			an.cursor = i
			return
		}
	}
	an.cursor = min(max(an.cursor, 0), len(stops)-1)
	an.curKey = stops[an.cursor].key
}

func (an *analysis) moveTo(stops []stop, i int) {
	an.cursor, an.curKey, an.free = i, stops[i].key, false
}

// current mengembalikan item di posisi kursor, baik di jawaban AI maupun
// di tampilan data.
func (m *model) current(an *analysis) (stop, []stop, bool) {
	if an.showData {
		lines := an.dataStops()
		if len(lines) == 0 {
			return stop{}, nil, false
		}
		an.dataCursor = min(max(an.dataCursor, 0), len(lines)-1)
		it := an.snap.Items[an.snap.LineIDs[lines[an.dataCursor]]]
		return stop{path: it.Path, isDir: it.IsDir, size: it.Size}, nil, true
	}
	stops := m.answerStops(an)
	if len(stops) == 0 {
		return stop{}, nil, false
	}
	return stops[an.cursor], stops, true
}

func (m *model) handleAnalysisKey(key string) tea.Cmd {
	an := m.an
	page := max(m.listHeight()-1, 1)
	switch key {
	case "ctrl+c":
		return tea.Quit
	case "q", "esc":
		if an.phase == anCollect || an.phase == anWaiting {
			an.fail(context.Canceled)
		}
		m.an = nil
		return nil
	case "r":
		return m.startAnalysis(an.root)
	case "p":
		m.openSettings("", false)
		return nil
	case "v":
		if an.snap != nil {
			an.showData = !an.showData
			an.textOff, an.free = 0, false
		}
		return nil
	case "pgup":
		an.textOff, an.free = an.textOff-page, true
		return nil
	case "pgdown":
		an.textOff, an.free = an.textOff+page, true
		return nil
	}

	cur, stops, ok := m.current(an)
	if !ok {
		return nil
	}

	// Aksi yang sama di jawaban AI maupun di tampilan data.
	switch key {
	case " ", "space":
		if !an.gone[cur.path] {
			if an.marks[cur.path] {
				delete(an.marks, cur.path)
				delete(an.markItems, cur.path)
			} else {
				an.marks[cur.path] = true
				an.markItems[cur.path] = an.delItem(cur)
			}
		}
		return m.step(an, stops, 1, false)
	case "d", "delete":
		var items []delItem
		for _, p := range slices.Sorted(maps.Keys(an.markItems)) {
			if !an.gone[p] {
				items = append(items, an.markItems[p])
			}
		}
		if len(items) == 0 && !an.gone[cur.path] {
			items = append(items, an.delItem(cur))
		}
		m.popup = m.confirmDelete(items)
		return nil
	case "o":
		m.status = explorerSelect(cur.path)
		return nil
	case "e":
		return m.gotoExplorer(cur)
	case "down", "j", "tab":
		return m.step(an, stops, 1, true)
	case "up", "k", "shift+tab":
		return m.step(an, stops, -1, true)
	case "home", "g":
		return m.step(an, stops, -1<<30, false)
	case "end", "G":
		return m.step(an, stops, 1<<30, false)
	}

	if an.showData {
		if key == "enter" || key == "right" || key == "l" {
			return m.gotoExplorer(cur)
		}
		return nil
	}

	// Khusus jawaban AI: buka/tutup folder di dalam chat.
	switch key {
	case "enter", "right", "l":
		switch {
		case an.gone[cur.path]:
			m.status = "Item ini sudah dihapus"
		case cur.isDir:
			return m.toggleExp(cur.path)
		default:
			m.status = "Ini file; tekan o untuk membukanya di Explorer"
		}
	case "left", "h", "backspace":
		switch {
		case cur.parent >= 0:
			an.moveTo(stops, cur.parent)
		case an.exp[cur.path] != nil:
			delete(an.exp, cur.path)
		}
	}
	return nil
}

// step menggeser kursor sebanyak d. wrap true membuat kursor berputar dari
// akhir ke awal (dan sebaliknya); jika false kursor berhenti di ujung.
func (m *model) step(an *analysis, stops []stop, d int, wrap bool) tea.Cmd {
	cur, n := &an.cursor, len(stops)
	if an.showData {
		cur, n = &an.dataCursor, len(an.dataStops())
	}
	if n == 0 {
		return nil
	}
	i := *cur + d
	if wrap {
		i = ((i % n) + n) % n
	} else {
		i = min(max(i, 0), n-1)
	}
	if an.showData {
		an.dataCursor, an.free = i, false
	} else {
		an.moveTo(stops, i)
	}
	return nil
}

func (an *analysis) delItem(s stop) delItem {
	return delItem{name: an.rel(s.path), path: s.path, isDir: s.isDir, size: s.size, sizeText: format.Bytes(s.size)}
}

// gotoExplorer membuka item di explorer utama: folder dibuka, file dipilih
// di folder induknya. Tekan b untuk kembali ke hasil analisis.
func (m *model) gotoExplorer(s stop) tea.Cmd {
	if m.an.gone[s.path] {
		m.status = "Item ini sudah dihapus"
		return nil
	}
	m.an = nil
	dir, sel := s.path, ""
	if !s.isDir {
		dir, sel = filepath.Dir(s.path), filepath.Base(s.path)
	}
	m.remember[dir] = sel
	if dir == m.path {
		if i := m.indexOfRow(sel); i >= 0 {
			m.cursor, m.follow = i, true
		}
		return nil
	}
	return loadDir(dir)
}

// rel mengembalikan path relatif terhadap folder yang dianalisis.
func (an *analysis) rel(path string) string {
	r, err := filepath.Rel(an.root, path)
	if err != nil || r == "." {
		return path
	}
	return r
}

// chip menyusun tampilan satu penyebutan item: [path · ukuran], berwarna
// sesuai saran AI, dengan ▸/▾ untuk folder. Mengembalikan teks berwarna dan
// lebar tampilnya.
func (an *analysis) chip(mn analyze.Mention, selected bool, maxW int) (string, int) {
	name := an.rel(mn.Path)
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
	prefix := "["
	if an.marks[mn.Path] {
		prefix = "[✓ "
	}
	if room := maxW - runewidth.StringWidth(marker+prefix+tail+"]"); runewidth.StringWidth(name) > room {
		name = truncateLeft(name, max(room, 8))
	}
	plain := prefix + name + tail + "]"

	var style lipgloss.Style
	switch {
	case selected:
		style = chipSel
	case an.gone[mn.Path]:
		style = dimStyle
	case mn.Action == analyze.ActionDelete:
		style = chipDelete
	case mn.Action == analyze.ActionKeep:
		style = chipKeep
	default:
		style = chipReview
	}
	return dimStyle.Render(marker) + style.Render(plain), runewidth.StringWidth(marker + plain)
}

// layoutAnswer membungkus jawaban AI ke lebar w. Penyebutan item tidak
// dipotong di tengah, baris "• …" yang terbungkus menjorok ke dalam, dan isi
// folder yang dibuka tampil sebagai tree di bawah paragrafnya. stops berisi
// setiap posisi kursor sesuai urutan tampil.
func (m *model) layoutAnswer(an *analysis, w int) (lines []string, stops []stop) {
	var cur strings.Builder
	curW, lineStart, hang := 0, 0, 0
	pendingSpace, paraStart := false, true
	var opened []int // stop folder terbuka di paragraf ini; tree-nya ditulis di akhir paragraf
	nth := 0

	newLine := func(indent int) {
		lines = append(lines, cur.String())
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
	// endParagraph menutup baris saat ini lalu menulis tree folder yang
	// dibuka di paragraf ini, tepat di bawahnya.
	endParagraph := func() {
		newLine(0)
		for _, i := range opened {
			m.emitTree(an, &lines, &stops, i, stops[i].path, "    ", w)
		}
		opened = opened[:0]
		hang, pendingSpace, paraStart = 0, false, true
	}

	for _, sg := range an.res.Segments() {
		if sg.ID > 0 {
			mn, ok := an.res.Mentions[sg.ID]
			if !ok {
				continue
			}
			s := mentionStop(nth, mn, 0)
			nth++
			text, width := an.chip(mn, s.key == an.curKey, w-hang)
			place(text, width)
			s.line = len(lines)
			stops = append(stops, s)
			if mn.IsDir && an.exp[mn.Path] != nil {
				opened = append(opened, len(stops)-1)
			}
			paraStart = false
			continue
		}
		for i, part := range strings.Split(sg.Text, "\n") {
			if i > 0 {
				endParagraph()
			}
			for j, word := range strings.Split(part, " ") {
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
	}
	endParagraph() // baris baru kosong setelahnya tidak ditampilkan
	an.syncCursor(stops)
	return lines, stops
}

func (m *model) viewAnalysis(b *strings.Builder) {
	an := m.an
	w := m.screenWidth()
	m.line(b, titleStyle.Render("Analisis AI · "+an.root))

	var status string
	switch an.phase {
	case anCollect:
		status = fmt.Sprintf("Mengumpulkan data… %s file · %s · %.0f detik · esc batal",
			format.Int(an.prog.Files.Load()), format.Bytes(an.prog.Bytes.Load()), time.Since(an.start).Seconds())
	case anWaiting:
		status = fmt.Sprintf("Menunggu jawaban AI (%s)… %.0f detik · %s baris dikirim · esc batal",
			an.model, time.Since(an.start).Seconds(), format.Int(int64(an.snap.Lines)))
	case anDone:
		status = fmt.Sprintf("Selesai dalam %.0f detik · %s · %s baris dikirim · %d folder/file disebut",
			an.elapsed.Seconds(), an.model, format.Int(int64(an.snap.Lines)), len(an.res.Mentions))
	case anFailed:
		status = warnStyle.Render("Gagal: " + an.err.Error())
	}
	m.line(b, dimStyle.Render(status))
	b.WriteString("\n")

	h := m.listHeight()
	cursorLine := -1
	var lines []string
	switch {
	case an.showData:
		lines = m.dataLines(an)
		if stops := an.dataStops(); len(stops) > 0 {
			cursorLine = stops[min(an.dataCursor, len(stops)-1)]
		}
	case an.phase == anDone:
		an.lastW = max(w-2, 20)
		var stops []stop
		lines, stops = m.layoutAnswer(an, an.lastW)
		if len(stops) > 0 {
			cursorLine = stops[an.cursor].line
		}
	case an.snap != nil:
		lines = []string{dimStyle.Render(fmt.Sprintf(
			"Data: %s baris mewakili %s file · %s folder · %s. Tekan v untuk melihatnya.",
			format.Int(int64(an.snap.Lines)), format.Int(an.snap.Files), format.Int(an.snap.Dirs), format.Bytes(an.snap.Size)))}
	}
	if an.phase == anFailed && !an.showData {
		// Pesan error lengkap, dibungkus agar tidak terpotong di baris status.
		lines = append(lines, "")
		for _, l := range strings.Split(ansi.Wrap("Gagal: "+an.err.Error(), max(w-2, 20), ""), "\n") {
			lines = append(lines, warnStyle.Render(l))
		}
		hint := "Tekan r untuk mencoba lagi atau esc untuk kembali."
		if errors.Is(an.err, analyze.ErrInvalidKey) {
			hint = "API key ditolak. Tekan p untuk mengganti API key, model, atau provider, lalu r untuk mencoba lagi."
		}
		lines = append(lines, "", dimStyle.Render(hint))
		if an.snap != nil {
			lines = append(lines, dimStyle.Render("Data folder tetap bisa dipakai: tekan v untuk memilih, membuka, atau menghapus isinya."))
		}
	}
	if !an.free && cursorLine >= 0 {
		if cursorLine < an.textOff {
			an.textOff = cursorLine
		}
		if cursorLine >= an.textOff+h {
			an.textOff = cursorLine - h + 1
		}
	}
	an.textOff = min(max(an.textOff, 0), max(len(lines)-h, 0))
	for i := range h {
		if j := an.textOff + i; j < len(lines) {
			m.line(b, " "+lines[j])
		} else {
			b.WriteString("\n")
		}
	}

	status = warnStyle.Render(m.status)
	switch n, size := an.marked(); {
	case m.status != "":
	case n > 0:
		status = markStyle.Render(fmt.Sprintf("%d ditandai · %s · d hapus · space lepas tanda", n, format.Bytes(size)))
	case len(lines) > h:
		status = dimStyle.Render(fmt.Sprintf("baris %d–%d dari %d", an.textOff+1, min(an.textOff+h, len(lines)), len(lines)))
	}
	m.line(b, status)
	keyText := keysAnalysis
	if an.showData {
		keyText = keysData
	}
	keys := dimStyle.Render(keyText)
	if lipgloss.Width(keys) > w {
		keys = truncateANSI(keys, w)
	}
	b.WriteString(keys)
}

// marked mengembalikan jumlah dan total ukuran item yang ditandai.
func (an *analysis) marked() (n int, size int64) {
	for p, it := range an.markItems {
		if !an.gone[p] {
			n++
			size += it.size
		}
	}
	return n, size
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
