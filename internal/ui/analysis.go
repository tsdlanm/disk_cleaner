package ui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsdlanm/cli_app/internal/analyze"
	"github.com/tsdlanm/cli_app/internal/format"
)

type anPhase int

const (
	anCollect anPhase = iota // memindai folder (lokal, tanpa AI)
	anConfirm                // data siap; menunggu pengguna setuju mengirim ke AI
	anWaiting                // menunggu jawaban analisis pertama
	anDone
	anFailed
)

// entryKind adalah jenis satu entri di percakapan.
type entryKind int

const (
	entUser   entryKind = iota // pertanyaan pengguna
	entTool                    // langkah alat yang dijalankan AI
	entAnswer                  // jawaban AI, dengan penyebutan item
	entError
	entNote // catatan dari aplikasi (bantuan, pertanyaan dibatalkan)
)

// entry adalah satu entri di percakapan.
type entry struct {
	kind entryKind
	text string          // pertanyaan, label alat, atau pesan
	sum  string          // alat: hasil singkat
	done bool            // alat: sudah selesai
	res  *analyze.Result // jawaban
}

// stepFeed menampung langkah alat dari goroutine AI untuk dibaca layar.
type stepFeed struct {
	mu    sync.Mutex
	steps []analyze.Step
}

func (f *stepFeed) push(s analyze.Step) {
	f.mu.Lock()
	f.steps = append(f.steps, s)
	f.mu.Unlock()
}

func (f *stepFeed) drain() []analyze.Step {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.steps
	f.steps = nil
	return out
}

// analysis adalah satu analisis AI beserta percakapan lanjutannya.
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
	res      *analyze.Result // jawaban analisis pertama
	err      error
	start    time.Time
	elapsed  time.Duration

	// Percakapan.
	entries   []entry
	sess      *analyze.Session
	input     []rune
	hist      [][]rune // pertanyaan sebelumnya; ↑ ↓ di kotak ketik
	histPos   int
	busy      bool // menunggu jawaban pertanyaan lanjutan
	qSeq      int  // nomor pertanyaan; jawaban dari pertanyaan lama diabaikan
	qStart    time.Time
	qCancel   context.CancelFunc
	cancelled bool // pembatalan sudah diminta
	feed      *stepFeed
	toolRow   map[int]int // nomor langkah -> indeks entri alat

	// Tampilan.
	itemsFocus bool                  // fokus di item yang disebut, bukan di kotak ketik
	cursor     int                   // indeks ke stop
	curKey     string                // menjaga kursor di item yang sama saat tampilan berubah
	lastW      int                   // lebar teks terakhir, untuk menyusun stop saat tombol ditekan
	exp        map[string]*expansion // folder yang dibuka di dalam jawaban
	dataCursor int                   // posisi kursor di tampilan data (indeks dataStops)
	textOff    int                   // baris teratas yang terlihat
	free       bool                  // pengguna menggulir sendiri; jangan paksa kursor terlihat
	stick      bool                  // ikuti ujung bawah percakapan
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
	// chatMsg adalah jawaban pertanyaan lanjutan nomor q.
	chatMsg struct {
		id, q int
		res   *analyze.Result
		err   error
	}
	anTickMsg struct{ id int }
)

// maxInput membatasi panjang pertanyaan.
const maxInput = 2000

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

// analyzePath memulai analisis baru untuk root; analisis sebelumnya tidak
// disimpan. Folder dipindai lebih dulu secara lokal dan baru dikirim ke AI
// setelah pengguna setuju. Tanpa API key, Pengaturan AI dibuka lebih dulu.
func (m *model) analyzePath(root string) tea.Cmd {
	if m.cfg.NeedsKey() {
		m.openSettings(root, true)
		return nil
	}
	return m.startAnalysis(root)
}

// sameDir membandingkan dua path folder tanpa membedakan huruf besar/kecil.
func sameDir(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(filepath.Clean(a), `\`), strings.TrimRight(filepath.Clean(b), `\`))
}

func (m *model) startAnalysis(root string) tea.Cmd {
	p, err := m.newProvider(m.cfg)
	if err != nil {
		m.status = "AI belum siap: " + err.Error()
		return nil
	}
	m.closeAnalysis()
	ctx, cancel := context.WithCancel(context.Background())
	m.anSeq++
	an := &analysis{
		id: m.anSeq, root: root, model: m.cfg.ModelName(), phase: anCollect,
		ctx: ctx, cancel: cancel, provider: p, prog: &analyze.Progress{}, start: time.Now(),
		entries: []entry{{kind: entUser, text: "Analisis " + truncateLeft(root, 70)}}, stick: true,
		feed: &stepFeed{}, toolRow: make(map[int]int),
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

// closeAnalysis membatalkan pekerjaan analisis yang berjalan dan membuangnya;
// percakapan tidak disimpan.
func (m *model) closeAnalysis() {
	if an := m.lastAn; an != nil {
		an.cancel()
		if an.qCancel != nil {
			an.qCancel()
		}
	}
	m.an, m.lastAn = nil, nil
}

// confirmNote adalah catatan di layar konfirmasi: apa yang sudah dipindai dan
// apa yang akan dikirim ke AI jika pengguna setuju.
func (an *analysis) confirmNote() string {
	s := an.snap
	return fmt.Sprintf("Folder selesai dipindai di komputer ini: %s file · %s. Belum ada yang dikirim ke AI.\n"+
		"Jika dikirim ke %s: %s item terbesar dinilai satu per satu (hapus / cek / simpan) dari %s baris data "+
		"(perkiraan %s token), hanya nama, ukuran, dan tanggal, tidak pernah isi file.\n"+
		"enter kirim · v lihat data yang akan dikirim · esc batal",
		format.Int(s.Files), format.Bytes(s.Size), an.model, format.Int(int64(len(s.Candidates))),
		format.Int(int64(s.Lines)), format.Int(int64(len(analyze.UserPrompt(s))/3)))
}

// sendAnalysis mengirim data folder ke AI setelah pengguna setuju.
func (m *model) sendAnalysis(an *analysis) tea.Cmd {
	an.phase, an.start = anWaiting, time.Now()
	an.stick, an.free = true, false
	if n := len(an.entries); n > 0 && an.entries[n-1].kind == entNote {
		an.entries = an.entries[:n-1] // catatan konfirmasi sudah terjawab
	}
	ctx, p, s, id := an.ctx, an.provider, an.snap, an.id
	run := func() tea.Msg {
		res, err := analyze.Analyze(ctx, p, s)
		return aiMsg{id: id, res: res, err: err}
	}
	return tea.Batch(run, anTick(id))
}

// fail mengakhiri analisis dengan error, lengkap dengan petunjuk langkah berikutnya.
func (an *analysis) fail(err error) {
	an.phase, an.elapsed = anFailed, time.Since(an.start)
	if errors.Is(err, context.Canceled) {
		err = errors.New("analisis dibatalkan")
	}
	an.err = err
	an.cancel()
	hint := "Ketik /ulang untuk mencoba lagi."
	if errors.Is(err, analyze.ErrInvalidKey) {
		hint = "API key ditolak. Ketik /model untuk mengganti API key, model, atau provider, lalu /ulang."
	}
	if an.snap != nil {
		hint += " Data folder tetap bisa dilihat dengan /data."
	}
	an.entries = append(an.entries,
		entry{kind: entError, text: "Gagal: " + err.Error()},
		entry{kind: entNote, text: hint})
	an.stick, an.free = true, false
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
		an.snap, an.phase = msg.snap, anConfirm
		an.entries = append(an.entries, entry{kind: entNote, text: an.confirmNote()})
		an.stick, an.free = true, false
		return nil

	case aiMsg:
		if an == nil || an.id != msg.id || an.phase != anWaiting {
			return nil
		}
		if msg.err != nil {
			an.fail(msg.err)
		} else {
			an.res, an.phase, an.elapsed = msg.res, anDone, time.Since(an.start)
			an.entries = append(an.entries, entry{kind: entAnswer, res: msg.res})
			an.sess = analyze.NewSession(an.snap, msg.res, m.sz.Measure)
			an.cursor, an.curKey = 0, "m:1:0"
			// Hasil dibaca dari atas: ringkasan dulu, bukan ujung bawah daftar.
			an.textOff, an.stick, an.free = 0, false, true
			an.cancel()
		}

	case chatMsg:
		if an == nil || an.id != msg.id || msg.q != an.qSeq || !an.busy {
			return nil
		}
		m.drainSteps(an)
		an.busy, an.cancelled = false, false
		an.qCancel()
		switch {
		case errors.Is(msg.err, context.Canceled):
			an.entries = append(an.entries, entry{kind: entNote, text: "Pertanyaan dibatalkan."})
		case msg.err != nil:
			an.entries = append(an.entries, entry{kind: entError, text: "Gagal: " + msg.err.Error()})
			if errors.Is(msg.err, analyze.ErrInvalidKey) {
				an.entries = append(an.entries, entry{kind: entNote, text: "API key ditolak. Ketik /model untuk menggantinya."})
			}
		default:
			an.entries = append(an.entries, entry{kind: entAnswer, res: msg.res})
		}
		an.stick, an.free = true, false

	case anTickMsg:
		if an != nil && an.id == msg.id {
			m.drainSteps(an)
			if an.phase == anCollect || an.phase == anWaiting || an.busy {
				return anTick(an.id)
			}
		}
	}
	return nil
}

// drainSteps memasukkan langkah alat yang baru jalan ke percakapan.
func (m *model) drainSteps(an *analysis) {
	for _, s := range an.feed.drain() {
		i, ok := an.toolRow[s.N]
		if !ok {
			i = len(an.entries)
			an.entries = append(an.entries, entry{kind: entTool})
			an.toolRow[s.N] = i
		}
		an.entries[i].text, an.entries[i].sum, an.entries[i].done = s.Label, s.Summary, s.Done
		an.stick = an.stick || !an.free
	}
}

// ask mengirim pertanyaan lanjutan. AI bisa memanggil alat baca-saja sebelum
// menjawab; langkahnya tampil di percakapan.
func (m *model) ask(an *analysis, question string) tea.Cmd {
	p, err := m.newProvider(m.cfg)
	if err != nil {
		an.entries = append(an.entries, entry{kind: entError, text: "AI belum siap: " + err.Error()})
		return nil
	}
	chat, ok := p.(analyze.Chatter)
	if !ok {
		an.entries = append(an.entries, entry{kind: entError, text: "Provider ini belum mendukung chat."})
		return nil
	}
	an.entries = append(an.entries, entry{kind: entUser, text: question})
	an.hist = append(an.hist, []rune(question))
	an.histPos = len(an.hist)

	ctx, cancel := context.WithCancel(context.Background())
	an.qSeq++
	an.busy, an.cancelled, an.qStart, an.qCancel = true, false, time.Now(), cancel
	an.toolRow = make(map[int]int)
	an.stick, an.free, an.itemsFocus = true, false, false

	sess, feed, id, q := an.sess, an.feed, an.id, an.qSeq
	run := func() tea.Msg {
		res, err := sess.Ask(ctx, chat, question, feed.push)
		return chatMsg{id: id, q: q, res: res, err: err}
	}
	return tea.Batch(run, anTick(id))
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

// transcriptStops menyusun posisi kursor di seluruh percakapan.
func (m *model) transcriptStops(an *analysis) []stop {
	w := an.lastW
	if w == 0 {
		w = max(m.screenWidth()-2, 20)
	}
	_, stops := m.layoutTranscript(an, w)
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

// current mengembalikan item di posisi kursor, baik di percakapan maupun di
// tampilan data.
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
	stops := m.transcriptStops(an)
	if len(stops) == 0 {
		return stop{}, nil, false
	}
	an.syncCursor(stops)
	return stops[an.cursor], stops, true
}

// handleAnalysisKey menangani tombol di layar analisis. Default-nya mengetik
// di kotak pertanyaan; Tab berpindah ke item yang disebut AI.
func (m *model) handleAnalysisKey(msg tea.KeyMsg) tea.Cmd {
	an := m.an
	m.status = ""
	if msg.Type == tea.KeyCtrlC {
		return tea.Quit
	}
	switch {
	case an.showData:
		return m.handleDataKey(an, msg.String())
	case an.phase == anConfirm:
		switch msg.String() {
		case "enter":
			return m.sendAnalysis(an)
		case "v":
			an.showData, an.textOff, an.free = true, 0, false
		case "esc", "q":
			m.closeAnalysis()
		case "pgup":
			m.scroll(an, -1)
		case "pgdown":
			m.scroll(an, 1)
		}
		return nil
	case an.phase == anCollect || an.phase == anWaiting:
		switch msg.String() {
		case "esc", "q":
			m.closeAnalysis()
		case "pgup":
			m.scroll(an, -1)
		case "pgdown":
			m.scroll(an, 1)
		}
		return nil
	case an.itemsFocus:
		return m.handleItemsKey(an, msg.String())
	}
	return m.handleInputKey(an, msg)
}

// scroll menggulir percakapan satu halaman ke atas (dir < 0) atau ke bawah.
func (m *model) scroll(an *analysis, dir int) {
	page := max(m.chatHeight()-1, 1)
	an.textOff += dir * page
	an.free = true
	an.stick = false
}

func (m *model) handleInputKey(an *analysis, msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		switch {
		case an.busy:
			if !an.cancelled {
				an.cancelled = true
				an.qCancel()
				m.status = "Membatalkan pertanyaan…"
			}
		case len(an.input) > 0:
			an.input, an.histPos = nil, len(an.hist)
		default:
			m.closeAnalysis() // percakapan tidak disimpan
		}
	case tea.KeyEnter:
		text := strings.TrimSpace(string(an.input))
		if an.busy || text == "" {
			return nil
		}
		an.input, an.histPos = nil, len(an.hist)
		if strings.HasPrefix(text, "/") {
			return m.runCommand(an, text)
		}
		if an.phase != anDone || an.sess == nil {
			m.status = "Chat tersedia setelah analisis berhasil"
			return nil
		}
		return m.ask(an, text)
	case tea.KeyTab:
		if stops := m.transcriptStops(an); len(stops) > 0 && !an.busy {
			an.itemsFocus = true
			an.syncCursor(stops)
			// Pilih item yang sedang terlihat, supaya layar tidak melompat.
			if l := stops[an.cursor].line; l < an.textOff || l >= an.textOff+m.chatHeight() {
				for i, s := range stops {
					if s.line >= an.textOff {
						an.cursor = i
						break
					}
				}
			}
			an.moveTo(stops, an.cursor)
		} else {
			m.status = "Belum ada item yang disebut AI"
		}
	case tea.KeyUp:
		if an.histPos > 0 && len(an.hist) > 0 {
			an.histPos--
			an.input = slices.Clone(an.hist[an.histPos])
		}
	case tea.KeyDown:
		if an.histPos < len(an.hist) {
			an.histPos++
			an.input = nil
			if an.histPos < len(an.hist) {
				an.input = slices.Clone(an.hist[an.histPos])
			}
		}
	case tea.KeyPgUp:
		m.scroll(an, -1)
	case tea.KeyPgDown:
		m.scroll(an, 1)
	case tea.KeyBackspace:
		if !an.busy && len(an.input) > 0 {
			an.input = an.input[:len(an.input)-1]
		}
	case tea.KeyCtrlU:
		if !an.busy {
			an.input = nil
		}
	case tea.KeyRunes, tea.KeySpace:
		if an.busy {
			return nil
		}
		// Karakter kontrol (baris baru dari paste, NUL) tidak ikut masuk.
		for _, r := range msg.Runes {
			if !unicode.IsControl(r) && len(an.input) < maxInput {
				an.input = append(an.input, r)
			}
		}
		an.stick, an.free = true, false
	}
	return nil
}

// anCommands adalah perintah yang diketik di kotak pertanyaan.
var anCommands = []struct{ name, desc string }{
	{"/data", "lihat data folder yang dikirim ke AI (item bisa dipilih)"},
	{"/ulang", "pindai ulang folder ini, lalu tanya lagi sebelum dikirim"},
	{"/bersih", "hapus pertanyaan lanjutan, kembali ke analisis awal"},
	{"/model", "ganti provider, model, atau API key"},
	{"/bantuan", "daftar perintah dan tombol"},
}

func (m *model) runCommand(an *analysis, text string) tea.Cmd {
	switch strings.Fields(text)[0] {
	case "/data":
		if an.snap == nil {
			m.status = "Data folder belum tersedia"
			return nil
		}
		an.showData, an.textOff, an.free = true, 0, false
	case "/ulang":
		return m.startAnalysis(an.root)
	case "/bersih":
		if an.sess == nil {
			m.status = "Belum ada percakapan"
			return nil
		}
		an.sess.Reset()
		an.entries = an.entries[:min(len(an.entries), 2)]
		an.toolRow = make(map[int]int)
		an.hist, an.histPos = nil, 0
		an.stick, an.free = true, false
		an.syncCursor(m.transcriptStops(an))
	case "/model", "/pengaturan":
		m.openSettings("", false)
	case "/bantuan":
		var b strings.Builder
		b.WriteString("Ketik pertanyaan lalu Enter. AI bisa melihat isi folder dan mencari file (baca-saja) bila perlu.\n")
		for _, c := range anCommands {
			fmt.Fprintf(&b, "%-9s %s\n", c.name, c.desc)
		}
		b.WriteString("Tab pindah ke item yang disebut AI: Enter buka folder, Space tandai, d hapus, e explorer.")
		an.entries = append(an.entries, entry{kind: entNote, text: b.String()})
		an.stick, an.free = true, false
	default:
		m.status = "Perintah tidak dikenal: " + strings.Fields(text)[0] + " · ketik /bantuan"
	}
	return nil
}

// handleItemsKey menangani tombol saat fokus di item yang disebut AI.
func (m *model) handleItemsKey(an *analysis, k string) tea.Cmd {
	switch k {
	case "esc", "tab":
		an.itemsFocus, an.stick, an.free = false, true, false
		return nil
	case "pgup":
		m.scroll(an, -1)
		return nil
	case "pgdown":
		m.scroll(an, 1)
		return nil
	}
	cur, stops, ok := m.current(an)
	if !ok {
		an.itemsFocus = false
		return nil
	}
	if cmd, done := m.itemAction(an, cur, stops, k); done {
		return cmd
	}

	switch k {
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

// handleDataKey menangani tombol di tampilan data folder.
func (m *model) handleDataKey(an *analysis, k string) tea.Cmd {
	switch k {
	case "esc", "q", "v":
		an.showData, an.textOff, an.free, an.stick = false, 0, false, true
		return nil
	case "pgup":
		m.scroll(an, -1)
		return nil
	case "pgdown":
		m.scroll(an, 1)
		return nil
	}
	cur, stops, ok := m.current(an)
	if !ok {
		return nil
	}
	if cmd, done := m.itemAction(an, cur, stops, k); done {
		return cmd
	}
	if k == "enter" || k == "right" || k == "l" {
		return m.gotoExplorer(cur)
	}
	return nil
}

// itemAction menjalankan aksi yang sama untuk item di percakapan maupun di
// tampilan data: tandai, hapus, buka di Explorer, pindah kursor.
func (m *model) itemAction(an *analysis, cur stop, stops []stop, k string) (tea.Cmd, bool) {
	switch k {
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
		return m.step(an, stops, 1, false), true
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
		return nil, true
	case "o":
		m.status = explorerSelect(cur.path)
		return nil, true
	case "e":
		return m.gotoExplorer(cur), true
	case "down", "j":
		return m.step(an, stops, 1, true), true
	case "up", "k", "shift+tab":
		return m.step(an, stops, -1, true), true
	case "home", "g":
		return m.step(an, stops, -1<<30, false), true
	case "end", "G":
		return m.step(an, stops, 1<<30, false), true
	}
	return nil, false
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
// di folder induknya. Analisisnya ditutup (tidak disimpan).
func (m *model) gotoExplorer(s stop) tea.Cmd {
	if m.an.gone[s.path] {
		m.status = "Item ini sudah dihapus"
		return nil
	}
	m.closeAnalysis()
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
