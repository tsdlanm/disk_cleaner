package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/tsdlanm/cli_app/internal/analyze"
	"github.com/tsdlanm/cli_app/internal/bin"
	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run menjalankan cmd (jika ada), memasukkan hasilnya ke model, lalu
// menjalankan cmd lanjutan. Tick diabaikan; pakai settle untuk menunggu.
func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil, tickMsg, anTickMsg, tea.QuitMsg:
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	default:
		_, next := m.Update(msg)
		run(m, next)
	}
}

func key(m *model, k string) {
	var msg tea.KeyMsg
	switch k {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	case "pgup":
		msg = tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		msg = tea.KeyMsg{Type: tea.KeyPgDown}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "space":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "ctrl+u":
		msg = tea.KeyMsg{Type: tea.KeyCtrlU}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	_, cmd := m.Update(msg)
	run(m, cmd)
}

// settle menunggu sampai semua ukuran folder di layar selesai dihitung.
func settle(t *testing.T, m *model) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.Update(tickMsg{})
		if m.pending == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ukuran folder tidak selesai dihitung")
}

func names(m *model) []string {
	var out []string
	for _, r := range m.rows {
		out = append(out, r.name)
	}
	return out
}

func TestNavigation(t *testing.T) {
	root := tempDir(t)
	write(t, filepath.Join(root, "big", "a.bin"), 5000)
	write(t, filepath.Join(root, "big", "sub", "b.bin"), 1000)
	write(t, filepath.Join(root, "small", "c.bin"), 100)
	write(t, filepath.Join(root, "file.txt"), 2000)

	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	run(m, loadDir(root))
	settle(t, m)

	if got, want := strings.Join(names(m), ","), "big,file.txt,small"; got != want {
		t.Fatalf("urutan = %s, want %s", got, want)
	}
	if m.total != 8100 {
		t.Errorf("total = %d, want 8100", m.total)
	}
	if v := m.View(); !strings.Contains(v, "big") || !strings.Contains(v, "5.9 KB") {
		t.Errorf("View tidak memuat baris big:\n%s", v)
	}

	// Urutannya selalu dari yang terbesar; tidak ada tombol untuk mengubahnya.
	key(m, "s")
	if got, want := strings.Join(names(m), ","), "big,file.txt,small"; got != want {
		t.Errorf("setelah menekan s: urutan = %s, want %s", got, want)
	}

	// Masuk ke big, lalu kembali: kursor kembali ke big.
	key(m, "enter")
	if m.path != filepath.Join(root, "big") {
		t.Fatalf("path = %s, want big (kursor %d, status %q)", m.path, m.cursor, m.status)
	}
	settle(t, m)
	if got, want := strings.Join(names(m), ","), "a.bin,sub"; got != want {
		t.Errorf("isi big = %s, want %s", got, want)
	}
	key(m, "left")
	if m.path != root || m.rows[m.cursor].name != "big" {
		t.Errorf("setelah kembali: path %s, kursor di %q", m.path, m.rows[m.cursor].name)
	}

	// Enter pada file tidak berpindah folder.
	key(m, "down")
	key(m, "enter")
	if m.path != root {
		t.Errorf("enter pada file mengubah path ke %s", m.path)
	}

}

// fakeAI menilai setiap item wajib dengan format JSON yang sama seperti AI
// sungguhan: hapus untuk node_modules, cek untuk yang lain.
// Untuk chat: pertanyaan berisi "isi" memicu alat list_dir, "gagal" memicu
// error, dan hasil alat dijawab dengan merujuk item pertamanya.
type fakeAI struct{ calls, chats int }

func (f *fakeAI) Complete(_ context.Context, _, user string, _ map[string]any) (string, error) {
	f.calls++
	_, list, _ := strings.Cut(user, "ITEM YANG HARUS DINILAI")
	var items []string
	for _, m := range regexp.MustCompile(`\[(\d+)\] (\S+)`).FindAllStringSubmatch(list, -1) {
		action, reason := "cek", "Isinya perlu dicek dulu."
		if strings.HasPrefix(m[2], "node_modules") {
			action, reason = "hapus", "Dependensi npm, bisa diinstal ulang."
		}
		items = append(items, `{"id":`+m[1]+`,"action":"`+action+`","reason":"`+reason+`"}`)
	}
	return `{"summary":"Sebagian besar ruang dipakai dependensi.","items":[` + strings.Join(items, ",") + `]}`, nil
}

func (f *fakeAI) Chat(_ context.Context, msgs []analyze.Message, tools []analyze.ToolDef) (analyze.Message, analyze.Usage, error) {
	f.chats++
	last := msgs[len(msgs)-1]
	u := analyze.Usage{In: 100, Out: 20, Requests: 1}
	reply := func(s string) (analyze.Message, analyze.Usage, error) {
		return analyze.Message{Role: "assistant", Content: s}, u, nil
	}
	switch {
	case last.Role == "tool":
		m := regexp.MustCompile(`\[(\d+)\] `).FindStringSubmatch(last.Content)
		if m == nil {
			return reply("Folder itu kosong.")
		}
		return reply("Isinya perlu dicek: [[" + m[1] + "|cek]].")
	case strings.Contains(last.Content, "gagal"):
		return analyze.Message{}, analyze.Usage{}, errors.New("kuota habis")
	case strings.Contains(last.Content, "isi") && len(tools) > 0:
		return analyze.Message{Role: "assistant", ToolCalls: []analyze.ToolCall{{
			ID: "c1", Type: "function", Function: analyze.ToolFunction{Name: "list_dir", Arguments: `{}`},
		}}}, u, nil
	}
	return reply("Jawaban singkat.")
}

func newAIModel(t *testing.T, ai analyze.Provider) *model {
	t.Helper()
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.cfg.APIKey = "test"
	m.newProvider = func(analyze.Config) (analyze.Provider, error) { return ai, nil }
	m.trash = func(paths []string) error {
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
		return nil
	}
	return m
}

// aiProject membuat folder uji: proyek web dengan node_modules dan backup, dan foto.
func aiProject(t *testing.T) string {
	t.Helper()
	root := tempDir(t)
	write(t, filepath.Join(root, "web", "node_modules", "pkg", "x.js"), 5<<20)
	write(t, filepath.Join(root, "web", "backup.zip"), 2<<20)
	write(t, filepath.Join(root, "photos", "a.jpg"), 3<<20)
	return root
}

// openAnalysis membuka folder web lalu menganalisisnya dengan a.
func openAnalysis(t *testing.T, ai analyze.Provider) (*model, *analysis, string) {
	t.Helper()
	root := aiProject(t)
	m := newAIModel(t, ai)
	run(m, loadDir(root))
	settle(t, m)
	key(m, "a") // kursor di folder web (terbesar)
	if m.an == nil || m.an.phase != anConfirm {
		t.Fatalf("a harus berhenti di konfirmasi: %+v", m.an)
	}
	key(m, "enter") // setuju mengirim ke AI
	if m.an == nil || m.an.phase != anDone {
		t.Fatalf("analisis tidak selesai: %+v", m.an)
	}
	return m, m.an, root
}

func TestAnalysis(t *testing.T) {
	ai := &fakeAI{}
	m, an, root := openAnalysis(t, ai)
	if an.root != filepath.Join(root, "web") || ai.calls != 1 {
		t.Fatalf("root %s, panggilan %d", an.root, ai.calls)
	}

	// Percakapan: permintaan analisis, lalu jawaban dengan item bersaran.
	v := m.View()
	for _, want := range []string{"Analisis AI", "› Analisis " + an.root[:20], "● Sebagian besar ruang dipakai dependensi",
		"✕ Aman dihapus · 1 item", "▸ node_modules\\", "Dependensi npm, bisa diinstal ulang.",
		"? Perlu dicek dulu · 1 item", "backup.zip", "2.0 MB", "Isinya perlu dicek dulu.", "ketik pertanyaan"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	// Setiap baris muat di layar; kotak ketik selebar layar penuh.
	for i, l := range strings.Split(v, "\n") {
		if w := ansi.StringWidth(l); w > 120 {
			t.Errorf("baris %d selebar %d > 120: %q", i, w, l)
		}
	}
	if !strings.Contains(v, "╭") || !strings.Contains(v, "╰") {
		t.Errorf("kotak ketik tidak tampil:\n%s", v)
	}

	// Tab: fokus ke item yang dinilai. Dua item sesuai urutan jawaban.
	key(m, "tab")
	nm, stops, _ := m.current(an)
	backup := filepath.Join(an.root, "backup.zip")
	if !an.itemsFocus || len(stops) != 2 || stops[1].path != backup {
		t.Fatalf("itemsFocus %v, stops %+v", an.itemsFocus, stops)
	}
	if filepath.Base(nm.path) != "node_modules" || nm.action != analyze.ActionDelete || nm.reason == "" {
		t.Fatalf("penyebutan pertama = %+v", nm)
	}
	// Detail item terpilih: saran, path, ukuran, dan alasannya.
	if v := m.View(); !strings.Contains(v, "✕ kemungkinan aman · node_modules\\ · 5.0 MB — Dependensi npm") {
		t.Errorf("detail item tidak tampil:\n%s", v)
	}

	// ↓ berpindah antar penyebutan dan berputar.
	key(m, "down")
	if sel, _, _ := m.current(an); sel.path != backup {
		t.Errorf("down: terpilih %s, want backup.zip", sel.path)
	}
	key(m, "down")
	if an.cursor != 0 {
		t.Errorf("down kedua: cursor %d, want 0", an.cursor)
	}

	// Hapus penyebutan terpilih: path di jawaban berubah jadi "terhapus".
	key(m, "d")
	if m.popup == nil || len(m.popup.confirm) != 1 || m.popup.confirm[0] != nm.path {
		t.Fatalf("popup konfirmasi = %+v", m.popup)
	}
	key(m, "y")
	if !an.gone[nm.path] {
		t.Error("item tidak ditandai terhapus")
	}
	if _, err := os.Stat(nm.path); !os.IsNotExist(err) {
		t.Error("node_modules masih ada")
	}
	if v := m.View(); !regexp.MustCompile(`node_modules\\ +terhapus`).MatchString(ansi.Strip(v)) {
		t.Errorf("View tidak menandai terhapus:\n%s", v)
	}

	// esc: kembali mengetik; esc lagi (kotak kosong) menutup dan membuang analisis.
	key(m, "esc")
	if an.itemsFocus || m.an != an {
		t.Fatalf("esc pertama: itemsFocus %v, layar %v", an.itemsFocus, m.an)
	}
	key(m, "esc")
	if m.an != nil || m.lastAn != nil {
		t.Fatal("esc tidak menutup layar analisis")
	}
}

// Pertanyaan lanjutan: AI memanggil alat baca-saja, langkahnya tampil, dan
// jawabannya merujuk item yang ditemukan alat.
func TestAnalysisChat(t *testing.T) {
	ai := &fakeAI{}
	m, an, _ := openAnalysis(t, ai)

	run(m, typeText(m, "apa isi folder ini?"))
	if an.busy || ai.chats != 2 {
		t.Fatalf("busy %v, panggilan chat %d (alat lalu jawaban)", an.busy, ai.chats)
	}
	v := m.View()
	for _, want := range []string{"› apa isi folder ini?", "● Melihat isi folder ini", "⎿ 2 item · 7.0 MB",
		"● Isinya perlu dicek: ▸ node_modules\\ · 5.0 MB.", "1 pertanyaan · 240 token"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	// Urutan entri: analisis, pertanyaan, langkah alat, jawaban.
	var kinds []entryKind
	for _, e := range an.entries {
		kinds = append(kinds, e.kind)
	}
	if !slices.Equal(kinds, []entryKind{entUser, entAnswer, entUser, entTool, entAnswer}) {
		t.Errorf("entri = %v", kinds)
	}
	if an.sess.Questions() != 1 || len(an.input) != 0 {
		t.Errorf("pertanyaan %d, input %q", an.sess.Questions(), string(an.input))
	}

	// Item di jawaban chat bisa dipilih seperti di jawaban pertama (3 penyebutan).
	key(m, "tab")
	key(m, "down")
	key(m, "down")
	cur, stops, _ := m.current(an)
	if len(stops) != 3 || cur.key != "m:4:0" || filepath.Base(cur.path) != "node_modules" || cur.action != analyze.ActionReview {
		t.Errorf("stops %d, kursor %+v", len(stops), cur)
	}
	key(m, "esc")

	// ↑ mengulang pertanyaan sebelumnya; ↓ mengosongkan lagi.
	key(m, "up")
	if string(an.input) != "apa isi folder ini?" {
		t.Errorf("riwayat: input %q", string(an.input))
	}
	key(m, "down")
	if len(an.input) != 0 {
		t.Errorf("↓ tidak mengosongkan input: %q", string(an.input))
	}

	// Pertanyaan biasa tanpa alat.
	run(m, typeText(m, "kenapa?"))
	if last := an.entries[len(an.entries)-1]; last.kind != entAnswer || !strings.Contains(last.res.Text, "Jawaban singkat") || ai.chats != 3 {
		t.Errorf("jawaban biasa: %+v, panggilan %d", last, ai.chats)
	}

	// Error dari layanan: tampil sebagai pesan, riwayat percakapan tidak rusak.
	run(m, typeText(m, "ini pasti gagal"))
	if last := an.entries[len(an.entries)-1]; last.kind != entError || !strings.Contains(last.text, "kuota habis") || an.busy {
		t.Errorf("error: %+v, busy %v", last, an.busy)
	}
	if an.sess.Questions() != 2 {
		t.Errorf("pertanyaan gagal ikut tercatat: %d", an.sess.Questions())
	}
}

// Ketikan: huruf masuk ke kotak, backspace dan ctrl+u menghapus, karakter
// kontrol (misalnya baris baru dari paste) dibuang, dan huruf yang sama
// berarti tombol pintas hanya setelah Tab ke item.
func TestAnalysisInput(t *testing.T) {
	m, an, _ := openAnalysis(t, &fakeAI{})

	// "d", "e", dan "o" adalah huruf biasa di kotak ketik, bukan hapus/explorer.
	for _, k := range []string{"d", "e", "o"} {
		key(m, k)
	}
	key(m, "space")
	if string(an.input) != "deo " || m.popup != nil || m.an != an {
		t.Fatalf("input %q, popup %v", string(an.input), m.popup)
	}
	key(m, "backspace")
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x\ny\x00z")})
	if string(an.input) != "deoxyz" {
		t.Errorf("setelah backspace dan paste: %q", string(an.input))
	}
	if v := m.View(); !strings.Contains(v, "› deoxyz") {
		t.Errorf("ketikan tidak tampil di kotak:\n%s", v)
	}
	key(m, "ctrl+u")
	if len(an.input) != 0 {
		t.Errorf("ctrl+u: %q", string(an.input))
	}

	// esc pada kotak yang berisi hanya mengosongkan, tidak menutup layar.
	key(m, "a")
	key(m, "esc")
	if m.an != an || len(an.input) != 0 {
		t.Errorf("esc: layar %v, input %q", m.an, string(an.input))
	}
	// Enter pada kotak kosong tidak mengirim apa pun.
	key(m, "enter")
	if len(an.entries) != 2 {
		t.Errorf("enter kosong menambah entri: %d", len(an.entries))
	}
	// Perintah yang diketik diberi petunjuk.
	key(m, "/")
	key(m, "d")
	if v := m.View(); !strings.Contains(v, "/data") {
		t.Errorf("petunjuk perintah tidak tampil:\n%s", v)
	}
}

func TestAnalysisCommands(t *testing.T) {
	ai := &fakeAI{}
	m, an, _ := openAnalysis(t, ai)
	run(m, typeText(m, "apa isi folder ini?"))
	if an.sess.Questions() != 1 {
		t.Fatal("pertanyaan tidak terkirim")
	}

	// /bantuan menambah catatan berisi daftar perintah.
	run(m, typeText(m, "/bantuan"))
	if last := an.entries[len(an.entries)-1]; last.kind != entNote || !strings.Contains(last.text, "/ulang") || !strings.Contains(last.text, "/data") {
		t.Errorf("/bantuan: %+v", last)
	}
	run(m, typeText(m, "/tidakada"))
	if !strings.Contains(m.status, "Perintah tidak dikenal") {
		t.Errorf("status = %q", m.status)
	}

	// /data: tampilan data folder; item bisa dipilih; esc kembali ke percakapan.
	run(m, typeText(m, "/data"))
	if !an.showData || !strings.Contains(m.View(), "tampilan data folder") {
		t.Fatalf("showData %v:\n%s", an.showData, m.View())
	}
	first, _, _ := m.current(an)
	key(m, "down")
	if second, _, _ := m.current(an); first.path == second.path {
		t.Errorf("down di data tidak berpindah: %s", first.path)
	}
	key(m, "esc")
	if an.showData || m.an != an {
		t.Errorf("esc dari data: showData %v, layar %v", an.showData, m.an)
	}

	// /bersih: kembali ke analisis awal; pertanyaan dan hasil alat dibuang.
	run(m, typeText(m, "/bersih"))
	if len(an.entries) != 2 || an.sess.Questions() != 0 || len(an.hist) != 0 {
		t.Errorf("/bersih: %d entri, %d pertanyaan", len(an.entries), an.sess.Questions())
	}

	// /ulang: memindai ulang dan bertanya lagi; AI baru dipanggil setelah setuju.
	callsBefore := ai.calls
	run(m, typeText(m, "/ulang"))
	if m.an == an || m.an.phase != anConfirm || ai.calls != callsBefore {
		t.Fatalf("/ulang: layar baru %v, fase %v, panggilan %d", m.an != an, m.an.phase, ai.calls)
	}
	key(m, "enter")
	if m.an.phase != anDone || ai.calls != callsBefore+1 {
		t.Errorf("setelah setuju: fase %v, panggilan %d", m.an.phase, ai.calls)
	}
}

// a selalu memindai folder lebih dulu dan tidak mengirim apa pun ke AI sebelum
// pengguna setuju; esc membatalkan tanpa memakai token.
func TestAnalysisConfirm(t *testing.T) {
	ai := &fakeAI{}
	root := aiProject(t)
	m := newAIModel(t, ai)
	run(m, loadDir(root))
	settle(t, m)

	key(m, "a")
	an := m.an
	if an == nil || an.phase != anConfirm || ai.calls != 0 || an.snap == nil {
		t.Fatalf("setelah a: %+v, panggilan %d", an, ai.calls)
	}
	v := m.View()
	for _, want := range []string{"Belum ada yang dikirim ke AI", "enter kirim", "v lihat data"} {
		if !strings.Contains(v, want) {
			t.Errorf("layar konfirmasi tidak memuat %q:\n%s", want, v)
		}
	}
	// v menampilkan data yang akan dikirim; esc kembali ke konfirmasi, belum mengirim.
	key(m, "v")
	if !an.showData || ai.calls != 0 {
		t.Fatalf("v: showData %v, panggilan %d", an.showData, ai.calls)
	}
	key(m, "esc")
	if an.showData || m.an != an || an.phase != anConfirm {
		t.Fatalf("esc dari data: showData %v, fase %v", an.showData, an.phase)
	}
	// esc membatalkan dan membuang analisis; tidak ada yang tersimpan.
	key(m, "esc")
	if m.an != nil || m.lastAn != nil || ai.calls != 0 {
		t.Fatalf("esc: an %v, lastAn %v, panggilan %d", m.an, m.lastAn, ai.calls)
	}

	// a lagi memulai dari awal (tidak ada riwayat), dan enter mengirim.
	key(m, "a")
	if m.an == an || m.an.phase != anConfirm {
		t.Fatalf("a kedua: layar sama %v, fase %v", m.an == an, m.an.phase)
	}
	key(m, "enter")
	if m.an.phase != anDone || ai.calls == 0 {
		t.Errorf("setelah enter: fase %v, panggilan %d", m.an.phase, ai.calls)
	}
	// Menutup analisis membuangnya; tombol b tidak lagi ada.
	key(m, "esc")
	key(m, "b")
	if m.an != nil || m.lastAn != nil {
		t.Errorf("analisis masih tersimpan: an %v, lastAn %v", m.an, m.lastAn)
	}
}

// typeText mengetik/menempel teks lalu menekan enter.
func typeText(m *model, s string) tea.Cmd {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return cmd
}

func TestAnalysisNeedsKey(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	root := tempDir(t)
	write(t, filepath.Join(root, "node_modules", "x.js"), 2<<20)

	ai := &fakeAI{}
	m := newAIModel(t, ai)
	m.cfg.APIKey = ""
	run(m, loadDir(root))
	settle(t, m)

	// a tanpa API key membuka Pengaturan AI langsung di input API key.
	key(m, "a")
	if m.settings == nil || !m.settings.editing || m.settings.current() != fieldKey || m.an != nil {
		t.Fatalf("settings %+v, an %v; want input API key dulu", m.settings, m.an)
	}
	// Key apa pun diterima apa adanya; enter menyimpan lalu memindai folder,
	// dan baru mengirim ke AI setelah pengguna setuju.
	run(m, typeText(m, "x"))
	if m.settings != nil || m.an == nil || m.an.phase != anConfirm || m.cfg.APIKey != "x" || ai.calls != 0 {
		t.Fatalf("setelah enter: settings %v, an %+v, key %q", m.settings, m.an, m.cfg.APIKey)
	}
	key(m, "enter")
	if m.an.phase != anDone {
		t.Fatalf("setelah setuju: fase %v", m.an.phase)
	}
	data, err := os.ReadFile(filepath.Join(appData, "scan", "config.json"))
	if err != nil || !strings.Contains(string(data), `"api_key": "x"`) {
		t.Errorf("API key tidak tersimpan: %s, %v", data, err)
	}
}

func TestSettings(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	m := newModel(sizer.New())
	m.cfg.APIKey = "key-lama"

	key(m, "p")
	if m.settings == nil || !strings.Contains(m.View(), "Pengaturan AI") {
		t.Fatalf("p tidak membuka pengaturan:\n%s", m.View())
	}
	// Provider: Gemini → Claude; model ikut ke bawaan Claude.
	key(m, "right")
	if s := m.settings.cfg; s.Provider != "claude" || s.ModelName() != "claude-opus-5-5" {
		t.Fatalf("provider %q model %q", s.Provider, s.ModelName())
	}
	// Model: pilih berikutnya dari daftar.
	key(m, "down")
	key(m, "right")
	if got := m.settings.cfg.ModelName(); got != "claude-sonnet-5-5" {
		t.Errorf("model = %q", got)
	}
	// API key: enter, ketik, enter. Karakter NUL (kasus nyata dari paste)
	// tidak ikut masuk.
	key(m, "down")
	key(m, "enter")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU}) // kosongkan dulu
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}})
	run(m, typeText(m, "key-baru-apa-saja"))
	// esc menyimpan dan menutup.
	key(m, "esc")
	if m.settings != nil || m.cfg.Provider != "claude" || m.cfg.Model != "claude-sonnet-5-5" || m.cfg.APIKey != "key-baru-apa-saja" {
		t.Errorf("tersimpan: settings %v cfg %+v", m.settings, m.cfg)
	}
	if got, _ := analyze.LoadConfig(); got.APIKey != "key-baru-apa-saja" {
		t.Errorf("config.json: %+v", got)
	}

	// Lainnya (OpenAI-compatible): muncul baris Base URL; model diketik sendiri.
	key(m, "p")
	key(m, "right")
	s := m.settings
	if s.cfg.Provider != "custom" || len(s.fields()) != 4 || !strings.Contains(m.View(), "Base URL") {
		t.Fatalf("custom: %+v\n%s", s.cfg, m.View())
	}
	key(m, "down") // Base URL
	key(m, "enter")
	run(m, typeText(m, "https://openrouter.ai/api/v1"))
	key(m, "down") // Model
	key(m, "enter")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	run(m, typeText(m, "meta/llama-gratis"))
	key(m, "esc")
	if m.cfg.BaseURL != "https://openrouter.ai/api/v1" || m.cfg.Model != "meta/llama-gratis" || m.cfg.URL() != m.cfg.BaseURL {
		t.Errorf("custom tersimpan: %+v", m.cfg)
	}

	// Provider berputar kembali ke Gemini dengan model bawaannya.
	key(m, "p")
	key(m, "right")
	if m.settings.cfg.Provider != "gemini" || m.settings.cfg.ModelName() != analyze.DefaultGeminiModel {
		t.Errorf("kembali ke gemini: %+v", m.settings.cfg)
	}
	key(m, "esc")
}

func TestAnalysisOpen(t *testing.T) {
	root := tempDir(t)
	nm := filepath.Join(root, "proj", "node_modules")
	write(t, filepath.Join(nm, "react", "lib", "index.js"), 2<<20)
	write(t, filepath.Join(nm, "lodash", "lodash.js"), 1<<20)

	m := newAIModel(t, &fakeAI{})
	run(m, loadDir(root))
	settle(t, m)
	key(m, "a") // memindai folder proj
	key(m, "enter")
	an := m.an

	// Tab lalu enter pada node_modules membuka isinya di bawah paragrafnya.
	key(m, "tab")
	key(m, "enter")
	settle(t, m)
	if m.an != an || an.exp[nm] == nil {
		t.Fatalf("enter: an %v, exp %v", m.an, an.exp)
	}
	v := m.View()
	for _, want := range []string{"▾ node_modules\\", "├ ▸ react\\", "└ ▸ lodash\\"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	// Isi folder tepat di bawah nama foldernya, sebelum penjelasan AI.
	if i, j := strings.Index(v, "└ ▸ lodash\\"), strings.Index(v, "Dependensi npm"); i < 0 || j < 0 || i > j {
		t.Errorf("isi folder tidak di atas penjelasan:\n%s", v)
	}

	// ↓ masuk ke isi folder yang terbuka, lalu enter membuka subfolder.
	key(m, "down")
	cur, stops, _ := m.current(an)
	if cur.mention || cur.parent != 0 || filepath.Base(cur.path) != "react" {
		t.Fatalf("down: stop %+v", cur)
	}
	key(m, "enter")
	settle(t, m)
	if !strings.Contains(m.View(), "▸ lib\\") {
		t.Errorf("subfolder react tidak terbuka:\n%s", m.View())
	}
	if n := len(m.transcriptStops(an)); n <= len(stops) {
		t.Errorf("stop tidak bertambah setelah membuka subfolder: %d", n)
	}

	// ← kembali ke folder induk (node_modules), ← lagi menutupnya.
	key(m, "left")
	if cur, _, _ := m.current(an); cur.key != "m:1:0" {
		t.Errorf("left: kursor di %s, want m:1:0", cur.key)
	}
	key(m, "left")
	if an.exp[nm] != nil {
		t.Error("left kedua tidak menutup node_modules")
	}

	// e membuka folder di explorer utama dan menutup analisis (tidak disimpan).
	key(m, "e")
	if m.an != nil || m.lastAn != nil || m.path != nm {
		t.Errorf("e: an %v, path %s", m.an, m.path)
	}
}

// badKeyAI selalu menolak API key, seperti key yang salah.
type badKeyAI struct{ calls int }

func (b *badKeyAI) Complete(context.Context, string, string, map[string]any) (string, error) {
	b.calls++
	return "", analyze.ErrInvalidKey
}

func TestAnalysisFailedData(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	root := tempDir(t)
	write(t, filepath.Join(root, "proj", "node_modules", "x.js"), 3<<20)
	write(t, filepath.Join(root, "proj", "big.iso"), 2<<20)

	bad := &badKeyAI{}
	m := newAIModel(t, bad)
	run(m, loadDir(root))
	settle(t, m)
	key(m, "a")
	key(m, "enter") // setuju mengirim; AI menolak API key
	an := m.an
	v := m.View()
	if an.phase != anFailed || !strings.Contains(v, "API key ditolak") || !strings.Contains(v, "/model") || !strings.Contains(v, "ketik /ulang") {
		t.Fatalf("phase %v, view:\n%s", an.phase, v)
	}

	// Chat belum tersedia tanpa analisis yang berhasil.
	run(m, typeText(m, "halo"))
	if !strings.Contains(m.status, "setelah analisis berhasil") || an.busy || len(an.hist) != 0 {
		t.Errorf("status %q, busy %v", m.status, an.busy)
	}

	// /model: ganti API key, esc simpan, lalu /ulang memindai dan bertanya lagi.
	run(m, typeText(m, "/model"))
	if m.settings == nil {
		t.Fatal("/model tidak membuka pengaturan")
	}
	key(m, "down")
	key(m, "down")
	key(m, "enter")
	run(m, typeText(m, "kunci-baru"))
	key(m, "esc")
	run(m, typeText(m, "/ulang"))
	if bad.calls != 1 || m.an.phase != anConfirm || m.cfg.APIKey != "kunci-baru" {
		t.Fatalf("calls %d, fase %v, key %q", bad.calls, m.an.phase, m.cfg.APIKey)
	}
	key(m, "enter")
	if bad.calls != 2 || m.an.phase != anFailed {
		t.Errorf("calls %d, fase %v", bad.calls, m.an.phase)
	}
	an = m.an

	// /data: data folder bisa dipilih walaupun AI gagal.
	run(m, typeText(m, "/data"))
	if !an.showData {
		t.Fatal("/data tidak membuka data")
	}
	first, _, _ := m.current(an)
	key(m, "down")
	second, _, _ := m.current(an)
	if first.path == second.path || second.path == "" {
		t.Errorf("down di data tidak berpindah: %s -> %s", first.path, second.path)
	}
	// Pilih node_modules lalu enter: dibuka di explorer utama; analisis ditutup.
	for range len(an.dataStops()) {
		if cur, _, _ := m.current(an); filepath.Base(cur.path) == "node_modules" {
			break
		}
		key(m, "down")
	}
	key(m, "enter")
	if m.an != nil || m.lastAn != nil || filepath.Base(m.path) != "node_modules" {
		t.Fatalf("enter di data: an %v, path %s", m.an, m.path)
	}
}
func TestCursorWrap(t *testing.T) {
	root := tempDir(t)
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		write(t, filepath.Join(root, n), 10)
	}
	m := newModel(sizer.New())
	run(m, loadDir(root))

	key(m, "up")
	if m.cursor != 2 {
		t.Errorf("up dari atas: kursor %d, want 2", m.cursor)
	}
	key(m, "down")
	if m.cursor != 0 {
		t.Errorf("down dari bawah: kursor %d, want 0", m.cursor)
	}
}

func TestDelete(t *testing.T) {
	root := tempDir(t)
	write(t, filepath.Join(root, "a.txt"), 300)
	write(t, filepath.Join(root, "b.txt"), 200)
	write(t, filepath.Join(root, "c", "x.bin"), 100)

	m := newModel(sizer.New())
	var moved []string
	m.trash = func(paths []string) error {
		moved = append(moved, paths...)
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
		return nil
	}
	run(m, loadDir(root))
	settle(t, m)

	// Tanpa tanda: yang dihapus item di kursor (a.txt, terbesar). n membatalkan.
	key(m, "d")
	if m.popup == nil || len(m.popup.confirm) != 1 || filepath.Base(m.popup.confirm[0]) != "a.txt" {
		t.Fatalf("popup konfirmasi = %+v", m.popup)
	}
	key(m, "n")
	if m.popup != nil || len(moved) != 0 {
		t.Fatalf("n: popup %v, moved %v", m.popup, moved)
	}

	// Tandai a.txt dan b.txt (space memindahkan kursor ke bawah), lalu hapus.
	key(m, " ")
	key(m, " ")
	if n, size := m.marked(); n != 2 || size != 500 {
		t.Fatalf("ditandai %d item %d byte, want 2 item 500 byte", n, size)
	}
	key(m, "d")
	if m.popup == nil || len(m.popup.confirm) != 2 {
		t.Fatalf("popup konfirmasi = %+v", m.popup)
	}
	key(m, "y")
	settle(t, m)

	if len(moved) != 2 {
		t.Errorf("moved = %v, want a.txt dan b.txt", moved)
	}
	if got := strings.Join(names(m), ","); got != "c" {
		t.Errorf("isi folder setelah hapus = %s, want c", got)
	}
	if len(m.marks) != 0 {
		t.Errorf("tanda tersisa: %v", m.marks)
	}
	if !strings.Contains(m.status, "2 item dipindahkan") {
		t.Errorf("status = %q", m.status)
	}
}

// recentReport membuat laporan dengan n file baru di setiap drive; file
// ke-i dibuat i jam lalu dan berukuran i+1 MB.
func recentReport(n int, roots ...string) *recent.Report {
	now := time.Now()
	rep := &recent.Report{}
	for _, root := range roots {
		d := recent.DriveReport{Root: root, SmallCount: 7, SmallSize: 70}
		for i := range n {
			d.Files = append(d.Files, recent.File{
				Path:    filepath.Join(root, fmt.Sprintf("proj%02d", i), "f.bin"),
				Size:    int64(i+1) << 20,
				Created: now.Add(-time.Duration(i)*time.Hour - time.Minute),
			})
			d.FileSize += int64(i+1) << 20
		}
		rep.Drives = append(rep.Drives, d)
	}
	return rep
}

func TestRecent(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 80})
	m.Update(recentMsg{rep: recentReport(30, `C:\`, `D:\`)})
	m.setPage(pageRecent)

	// "File baru" punya halaman sendiri (tab kedua): rentang bawaan 24 jam
	// terakhir, per drive, dari yang terbaru. Folder berisi satu file
	// langsung menampilkan nama file itu; path lengkapnya tampil di atas
	// bagian ini saat kursor di barisnya.
	if n := len(m.recentFiles()); n != 48 {
		t.Fatalf("file = %d, want 48", n)
	}
	v := m.View()
	for _, want := range []string{"File baru", "48 file", "dari yang terbaru", `C:\ · 24 file baru · 24 folder`, "f.bin", "1.0 MB"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "proj24") {
		t.Errorf("file lebih dari 24 jam ikut tampil:\n%s", v)
	}

	// Per drive hanya 10 folder tampil, lalu tombol "tampilkan lainnya".
	if n := strings.Count(v, "f.bin"); n != 20 || strings.Count(v, "tampilkan lainnya (14 folder lagi)") != 2 {
		t.Errorf("%d baris folder (want 20), tombol lainnya:\n%s", n, v)
	}

	// Kursor mulai di judul drive C; ↓ ke folder pertamanya.
	if l, _ := m.recentCurrent(); !m.focusRecent || l.place != placeTitle || l.drive != 0 {
		t.Fatalf("awal: fokus %v, baris %+v, want judul drive C", m.focusRecent, l)
	}
	key(m, "down")
	if !m.focusRecent || m.selectedRecent() != `C:\proj00` {
		t.Fatalf("↓: fokus %v, kursor %q", m.focusRecent, m.selectedRecent())
	}
	if v := m.View(); !strings.Contains(v, `C:\proj00\f.bin`) {
		t.Errorf("path lengkap file di kursor tidak tampil di atas:\n%s", v)
	}

	// Turun ke tombol "tampilkan lainnya" di drive C; enter menambah 10 folder,
	// kursor pindah ke folder pertama yang baru tampil.
	for range 10 {
		key(m, "down")
	}
	if l, _ := m.recentCurrent(); l.place != placeMore || l.drive != 0 {
		t.Fatalf("kursor di %+v, want tombol lainnya drive C", l)
	}
	if v := m.View(); !strings.Contains(v, "enter: tampilkan 10 folder lagi") {
		t.Errorf("petunjuk tombol tidak tampil:\n%s", v)
	}
	key(m, "enter")
	if m.shownFor(0) != 20 || m.shownFor(1) != 10 || m.selectedRecent() != `C:\proj10` {
		t.Errorf("setelah enter: C %d, D %d, kursor %q", m.shownFor(0), m.shownFor(1), m.selectedRecent())
	}
	if v := m.View(); !strings.Contains(v, "tampilkan lainnya (4 folder lagi)") {
		t.Errorf("sisa C setelah enter:\n%s", v)
	}

	// Urutannya selalu dari yang terbaru; tidak ada tombol untuk mengubahnya.
	key(m, "up")
	key(m, "s")
	if ps := m.places(); ps[0].dir != `C:\proj00` || m.selectedRecent() != `C:\proj09` {
		t.Errorf("urutan berubah: pertama %s, kursor %s", ps[0].dir, m.selectedRecent())
	}

	// esc kembali ke halaman Drive.
	key(m, "esc")
	if m.focusRecent || m.page != pageDrives {
		t.Error("esc tidak kembali ke daftar drive")
	}
}

func TestRecentScrollKeepsDriveTitle(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 14})
	m.Update(recentMsg{rep: recentReport(20, `C:\`, `D:\`)})
	m.setPage(pageRecent)
	for range 12 {
		key(m, "down")
	}
	if strings.Contains(m.View(), `C:\ · 20 file baru`) {
		t.Fatal("daftar tidak tergulir; tes tidak menguji apa-apa")
	}
	for range 11 {
		key(m, "up")
	}
	if l, _ := m.recentCurrent(); l.place < 0 || l.drive != 0 {
		t.Fatalf("kursor di %+v, want folder pertama drive C", l)
	}
	if v := m.View(); !strings.Contains(v, `C:\ · 20 file baru`) {
		t.Errorf("judul drive C hilang saat kursor di folder pertamanya:\n%s", v)
	}
}

// Judul drive di "File baru" bisa dipilih: enter menutup semua isinya sampai
// tinggal judul drive, enter lagi membukanya; ← juga menutup dari folder mana pun.
func TestRecentCollapseDrive(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 80})
	m.Update(recentMsg{rep: recentReport(12, `C:\`, `D:\`)})
	m.setPage(pageRecent)
	count := func() int { return strings.Count(m.View(), "f.bin") }
	if count() != 20 {
		t.Fatalf("awal: %d folder tampil, want 20", count())
	}

	key(m, "enter") // kursor sudah di judul drive C
	v := m.View()
	if count() != 10 || !strings.Contains(v, "▸ C:\\") || !strings.Contains(v, "▾ D:\\") || strings.Count(v, "(2 folder lagi)") != 1 {
		t.Errorf("C ditutup, D tetap terbuka (%d folder):\n%s", count(), v)
	}
	if l, _ := m.recentCurrent(); l.place != placeTitle || l.drive != 0 {
		t.Errorf("kursor setelah menutup di %+v, want judul C", l)
	}
	if !strings.Contains(m.View(), "enter: buka daftar folder C:\\") {
		t.Errorf("petunjuk buka tidak tampil:\n%s", m.View())
	}

	// Seluruhnya tutup: tinggal dua judul drive.
	key(m, "down") // judul D (C tidak punya baris lain)
	if l, _ := m.recentCurrent(); l.drive != 1 || l.place != placeTitle {
		t.Fatalf("↓ dari C yang ditutup ke %+v, want judul D", l)
	}
	key(m, "enter")
	if count() != 0 || len(m.recentLines()) != 2 {
		t.Errorf("semua ditutup: %d folder, %d baris", count(), len(m.recentLines()))
	}

	// enter membuka lagi; jumlah yang tampil kembali seperti semula.
	key(m, "enter")
	key(m, "up")
	key(m, "enter")
	if count() != 20 {
		t.Errorf("dibuka lagi: %d folder, want 20", count())
	}

	// ← dari folder menutup drive-nya dan kursor pindah ke judulnya.
	key(m, "down")
	key(m, "left")
	if l, _ := m.recentCurrent(); l.place != placeTitle || !m.rclosed[0] || count() != 10 {
		t.Errorf("←: kursor %+v, C ditutup %v, %d folder", l, m.rclosed[0], count())
	}
	// → membuka lagi; enter pada judul D tetap hanya D.
	key(m, "right")
	if m.rclosed[0] || count() != 20 {
		t.Errorf("→: C ditutup %v, %d folder", m.rclosed[0], count())
	}
	// ↑ dari judul drive pertama tetap di sana; esc kembali ke halaman Drive.
	key(m, "up")
	if !m.focusRecent || m.rcursor != 0 {
		t.Errorf("↑ dari judul pertama: fokus %v, kursor %d", m.focusRecent, m.rcursor)
	}
	key(m, "esc")
	if m.focusRecent || m.page != pageDrives {
		t.Error("esc tidak kembali ke halaman Drive")
	}
}

// enter pada judul drive menutup bertahap: dulu hasil "tampilkan lainnya"
// (10 folder per enter), baru setelah itu seluruh isinya.
func TestRecentCollapseSteps(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 90})
	m.Update(recentMsg{rep: recentReport(24, `C:\`)}) // 24 folder: semua masuk jendela 24 jam
	m.setPage(pageRecent)
	count := func() int { return strings.Count(m.View(), "f.bin") }

	// Buka sampai semua tampil: 10 → 20 → 24.
	for _, want := range []int{20, 24} { // kursor sudah di judul drive
		for range m.visibleCount(0) + 1 { // ke tombol "tampilkan lainnya"
			key(m, "down")
		}
		if l, _ := m.recentCurrent(); l.place != placeMore {
			t.Fatalf("kursor di %+v, want tombol lainnya", l)
		}
		key(m, "enter")
		if m.visibleCount(0) != want {
			t.Fatalf("tampil %d, want %d", m.visibleCount(0), want)
		}
		key(m, "g") // kembali ke judul drive
		if l, _ := m.recentCurrent(); l.place != placeTitle {
			t.Fatalf("kursor di %+v, want judul drive", l)
		}
	}
	if count() != 24 {
		t.Fatalf("semua tampil: %d, want 24", count())
	}
	if v := m.View(); !strings.Contains(v, "enter: kurangi jadi 20 folder") {
		t.Errorf("petunjuk ciut tidak tampil:\n%s", v)
	}

	// Tiap enter menutup satu tahap: 24 → 20 → 10 → tertutup semua.
	for _, want := range []int{20, 10, 0} {
		key(m, "enter")
		if count() != want {
			t.Fatalf("setelah enter: %d folder tampil, want %d", count(), want)
		}
		if l, _ := m.recentCurrent(); l.place != placeTitle {
			t.Fatalf("kursor pindah dari judul drive ke %+v", l)
		}
	}
	if !m.rclosed[0] || len(m.recentLines()) != 1 {
		t.Errorf("tidak tertutup penuh: ditutup %v, %d baris", m.rclosed[0], len(m.recentLines()))
	}

	// enter lagi membukanya: mulai dari 10 folder pertama.
	key(m, "enter")
	if count() != 10 || m.rclosed[0] {
		t.Errorf("dibuka lagi: %d folder, ditutup %v", count(), m.rclosed[0])
	}
}

func TestPlaceOf(t *testing.T) {
	home := homeDir()
	m := newModel(sizer.New())
	for path, want := range map[string]string{
		home + `\AppData\Local\go-build\64\64b5f7ba69df39c517e41e1a31ff3b0ece0b-d`:           home + `\AppData\Local\go-build`,
		home + `\AppData\Local\Google\Chrome\User Data\Profile 19\Cache\Cache_Data\f_01`:     home + `\AppData\Local\Google\Chrome`,
		home + `\AppData\Local\Temp\setup.log`:                                               home + `\AppData\Local\Temp`,
		home + `\.vscode\extensions\x\a.vsix`:                                                home + `\.vscode`,
		home + `\Downloads\film\a.mp4`:                                                       home + `\Downloads\film`,
		home + `\Downloads\a.iso`:                                                            home + `\Downloads`,
		`C:\Program Files (x86)\Microsoft\Copilot\Application\x\a.dll`:                       `C:\Program Files (x86)\Microsoft\Copilot`,
		`C:\ProgramData\Microsoft\Windows Defender\Definition Updates\{025D0774-64F1}\m.dll`: `C:\ProgramData\Microsoft\Windows Defender`,
		`C:\Windows\SoftwareDistribution\Download\a.cab`:                                     `C:\Windows\SoftwareDistribution`,
		`D:\anam\scan_cli\scan.exe`:                                                          `D:\anam\scan_cli`,
		`D:\a.iso`:                                                                           `D:\`,
	} {
		if got := m.placeOf(path); got != want {
			t.Errorf("placeOf(%s) = %s, want %s", path, got, want)
		}
	}
}

// Beberapa file di tempat yang sama menjadi satu baris berisi nama folder
// terluarnya saja; path lengkapnya tampil di atas saat kursor di baris itu,
// dan enter masuk ke sana dengan kursor di subfolder berisi file terbarunya.
func TestRecentPlaces(t *testing.T) {
	now := time.Now()
	m := newModel(sizer.New())
	m.dirExists = func(string) bool { return true } // folder rekaan tidak ada di disk
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	d := recent.DriveReport{Root: `D:\`}
	for i := range 8 {
		d.Files = append(d.Files, recent.File{Path: fmt.Sprintf(`D:\proyek\app\build\%d\f%d.bin`, i, i), Size: int64(i+1) << 20, Created: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	d.Files = append(d.Files, recent.File{Path: `D:\unduhan\film.mp4`, Size: 700 << 20, Created: now.Add(-time.Hour)})
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{d}}})
	m.setPage(pageRecent)

	v := m.View()
	for _, want := range []string{`D:\ · 9 file baru · 2 folder`, "app\\", "8 file", "36.0 MB", "film.mp4", "700.0 MB", "1 menit lalu"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "f7.bin") || strings.Contains(v, "build") || strings.Contains(v, `D:\proyek`) {
		t.Errorf("isi folder atau path lengkap ikut tampil di daftar:\n%s", v)
	}

	key(m, "down") // dari judul drive ke folder pertama
	if v := m.View(); !strings.Contains(v, `D:\proyek\app\`) {
		t.Errorf("path lengkap folder di kursor tidak tampil di atas:\n%s", v)
	}
	key(m, "enter")
	if m.remember[`D:\proyek\app`] != "build" {
		t.Errorf("enter: kursor diingat di %q, want build", m.remember[`D:\proyek\app`])
	}
}

// File sementara (misalnya folder go-build) bisa sudah hilang sebelum
// pemantauan mencatatnya: enter tidak error, tapi membersihkan daftar.
func TestRecentMissingFolder(t *testing.T) {
	root := tempDir(t)
	gone := filepath.Join(root, "go-build123", "b001", "_pkg_.a") // folder tidak pernah dibuat
	now := time.Now()
	m := newModel(sizer.New())
	m.dirExists = func(string) bool { return false } // tempatnya sudah hilang
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{{
		Root: filepath.VolumeName(root) + `\`, Files: []recent.File{{Path: gone, Size: 2 << 20, Created: now.Add(-time.Minute)}},
	}}}})
	if len(m.places()) != 1 {
		t.Fatalf("places = %d, want 1", len(m.places()))
	}

	m.setPage(pageRecent)
	key(m, "down") // folder yang sudah hilang
	key(m, "enter")
	if m.path != "" || !m.focusRecent {
		t.Errorf("enter membuka %q (fokus File baru %v); folder yang hilang tidak boleh dibuka", m.path, m.focusRecent)
	}
	if !strings.Contains(m.status, "sudah tidak ada") || strings.Contains(m.status, "Tidak bisa membuka") {
		t.Errorf("status = %q", m.status)
	}
	if len(m.places()) != 0 || len(m.recentFiles()) != 0 {
		t.Errorf("daftar tidak dibersihkan: %d tempat, %d file", len(m.places()), len(m.recentFiles()))
	}
}

func TestTruncateMiddle(t *testing.T) {
	if got := truncateMiddle("88809242bdf24896832995bb29dc7440", 12); got != "88809…dc7440" {
		t.Errorf("truncateMiddle = %q, want 88809…dc7440", got)
	}
}

func TestSnapshotStartup(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := tempDir(t)
	write(t, filepath.Join(root, "baru.iso"), 2<<20)
	write(t, filepath.Join(root, "lama.bin"), 3<<20) // file di hasil tersimpan harus masih ada
	drv := drivesMsg{{Root: root, Ready: true, Kind: "Lokal"}}
	snapPath := recent.SnapshotPath(dataDir())

	save := func(at time.Time) {
		t.Helper()
		err := recent.SaveSnapshot(snapPath, &recent.Snapshot{
			Time: at,
			Report: &recent.Report{Drives: []recent.DriveReport{{
				Root: root, Files: []recent.File{{Path: filepath.Join(root, "lama.bin"), Size: 3 << 20, Created: at}},
			}}},
			Dirs: map[string]sizer.Info{filepath.Join(root, "x"): {Size: 7 << 20}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	start := func() *model {
		m := newModel(sizer.New())
		m.autoRecent = true
		m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
		_, cmd := m.Update(drv)
		run(m, cmd)
		return m
	}

	// Hasil tersimpan yang masih baru: langsung tampil, tanpa memindai.
	save(time.Now().Add(-10 * time.Minute))
	m := start()
	if m.recentRunning || m.recent == nil || m.recent.Drives[0].Files[0].Path != filepath.Join(root, "lama.bin") {
		t.Fatalf("snapshot baru: running %v, recent %+v", m.recentRunning, m.recent)
	}
	if inf, st := m.sz.Get(filepath.Join(root, "x")); st != sizer.Done || inf.Size != 7<<20 {
		t.Errorf("ukuran folder dari snapshot tidak masuk cache: %+v %v", inf, st)
	}
	m.setPage(pageRecent)
	if v := m.View(); !strings.Contains(v, "dipindai 10 menit lalu") || !strings.Contains(v, "1 file") {
		t.Errorf("View:\n%s", v)
	}

	// Hasil tersimpan yang sudah lama: dipindai ulang, lalu disimpan lagi.
	os.Remove(filepath.Join(root, "lama.bin")) // hasil pindai baru hanya memuat baru.iso
	save(time.Now().Add(-7 * time.Hour))
	m = start()
	if m.recentRunning || len(m.recent.Drives[0].Files) != 1 || filepath.Base(m.recent.Drives[0].Files[0].Path) != "baru.iso" {
		t.Fatalf("pindai ulang: %+v", m.recent)
	}
	snap, err := recent.LoadSnapshot(snapPath)
	if err != nil || time.Since(snap.Time) > time.Minute {
		t.Errorf("snapshot tidak diperbarui: %v, %v", snap, err)
	}
}

func TestRecentOpenDelete(t *testing.T) {
	root := tempDir(t)
	dl := filepath.Join(root, "dl")
	iso := filepath.Join(dl, "big.iso")
	write(t, iso, 100)
	write(t, filepath.Join(dl, "lain.txt"), 10)

	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.cfg.RecentMinKB = 0 // file 100 byte tetap dihitung "baru" saat diperiksa ulang
	m.binList = func([]string) ([]bin.Item, error) { return nil, nil }
	m.trash = func(paths []string) error {
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
		return nil
	}
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{{
		Root: filepath.VolumeName(root) + `\`, Files: []recent.File{{Path: iso, Size: 100, Created: time.Now().Add(-3 * time.Hour)}},
	}}}})
	m.setPage(pageRecent)
	if v := m.View(); !strings.Contains(v, "1 file") || !strings.Contains(v, "3 jam lalu") {
		t.Errorf("View:\n%s", v)
	}

	// enter masuk ke folder tempat itu, kursor di subfolder berisi file baru;
	// esc kembali ke File baru.
	key(m, "down") // dari judul drive ke folder pertama
	place := m.places()[0].dir
	key(m, "enter")
	if !strings.EqualFold(m.path, place) || m.cursor >= len(m.rows) {
		t.Fatalf("enter: path %s, want %s", m.path, place)
	}
	if rel, _ := filepath.Rel(place, m.longHome(iso)); m.rows[m.cursor].name != strings.Split(rel, `\`)[0] {
		t.Errorf("kursor di %q, want awal %s", m.rows[m.cursor].name, rel)
	}
	key(m, "esc")
	if m.path != "" || !m.focusRecent {
		t.Fatalf("esc: path %q, fokus File baru %v", m.path, m.focusRecent)
	}

	// File dihapus dari explorer: langsung hilang dari File baru.
	run(m, loadDir(dl))
	m.cursor = m.indexOfRow("big.iso")
	key(m, "d")
	key(m, "y")
	if len(m.recentFiles()) != 0 {
		t.Errorf("file masih ada di File baru: %+v", m.recentFiles())
	}
}

func TestBinPage(t *testing.T) {
	now := time.Now()
	items := []bin.Item{
		{Original: `C:\Users\a\Downloads\win11.iso`, Info: `C:\$Recycle.Bin\S\$I1.iso`, Size: 5 << 30, Deleted: now.Add(-2 * time.Hour)},
		{Original: `D:\proyek\node_modules`, Info: `D:\$Recycle.Bin\S\$I2`, Size: 300 << 20, Deleted: now.Add(-time.Hour), IsDir: true},
	}
	var restored, purged []string
	emptied := false
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m.binList = func([]string) ([]bin.Item, error) {
		var out []bin.Item
		for _, it := range items {
			if !slices.Contains(restored, it.Info) && !slices.Contains(purged, it.Info) && !emptied {
				out = append(out, it)
			}
		}
		return out, nil
	}
	m.binRestore = func(it bin.Item) error { restored = append(restored, it.Info); return nil }
	m.binPurge = func(it bin.Item) error { purged = append(purged, it.Info); return nil }
	m.binEmpty = func() error { emptied = true; return nil }

	// tab dua kali: halaman Recycle Bin (setelah File baru), isinya dibaca saat dibuka.
	key(m, "tab")
	key(m, "tab")
	v := m.View()
	for _, want := range []string{"Recycle Bin (2 · 5.3 GB)", "✕ Kosongkan Recycle Bin · 2 item · 5.3 GB", "win11.iso", `node_modules\`, "dihapus 2 jam lalu", "5.0 GB"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	// Daftar hanya memuat nama; path lengkap item di kursor tampil di baris atas.
	if strings.Count(v, `C:\Users\a\Downloads\win11.iso`) != 1 {
		t.Errorf("path lengkap item di kursor harus tampil sekali (di atas):\n%s", v)
	}
	if strings.Contains(v, `D:\proyek\node_modules`) {
		t.Errorf("path lengkap item yang tidak dipilih ikut tampil:\n%s", v)
	}
	key(m, "down")
	if v := m.View(); !strings.Contains(v, `D:\proyek\node_modules\`) || strings.Contains(v, `C:\Users\a\Downloads`) {
		t.Errorf("baris atas tidak mengikuti kursor:\n%s", v)
	}
	key(m, "up")

	// u memulihkan item di kursor.
	key(m, "u")
	if len(restored) != 1 || len(m.binItems) != 1 || !strings.Contains(m.status, "1 item dipulihkan") {
		t.Fatalf("pulihkan: %v, sisa %d, status %q", restored, len(m.binItems), m.status)
	}

	// d meminta konfirmasi dulu; n membatalkan, y menghapus permanen.
	key(m, "d")
	if m.popup == nil || !strings.Contains(m.popup.title, "Hapus permanen 1 item") {
		t.Fatalf("popup = %+v", m.popup)
	}
	key(m, "n")
	if len(purged) != 0 {
		t.Fatal("n tetap menghapus")
	}
	key(m, "d")
	key(m, "y")
	if len(purged) != 1 || len(m.binItems) != 0 || !strings.Contains(m.View(), "Recycle Bin kosong") {
		t.Errorf("hapus permanen: %v, sisa %d\n%s", purged, len(m.binItems), m.View())
	}

	// E pada Recycle Bin kosong tidak membuka konfirmasi.
	key(m, "E")
	if m.popup != nil || emptied {
		t.Error("E pada Recycle Bin kosong")
	}
}

// Tombol "Kosongkan Recycle Bin" ada di atas daftar: ↑ dari item pertama ke
// sana, enter meminta konfirmasi lalu menghapus semuanya sekaligus.
func TestBinEmptyButton(t *testing.T) {
	now := time.Now()
	items := []bin.Item{
		{Original: `C:\a.iso`, Info: `C:\$Recycle.Bin\S\$I1`, Size: 3 << 20, Deleted: now},
		{Original: `C:\b.iso`, Info: `C:\$Recycle.Bin\S\$I2`, Size: 2 << 20, Deleted: now},
	}
	var purged []string
	emptied := false
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.binList = func([]string) ([]bin.Item, error) {
		if emptied {
			return nil, nil
		}
		return items, nil
	}
	m.binPurge = func(it bin.Item) error { purged = append(purged, it.Info); return nil }
	m.binEmpty = func() error { emptied = true; return nil }
	key(m, "tab")
	key(m, "tab")

	// Kursor awal di item pertama; ↑ ke tombol, dan baris atas menjelaskannya.
	if m.bcursor != 0 {
		t.Fatalf("kursor awal = %d, want 0", m.bcursor)
	}
	key(m, "up")
	if m.bcursor != binButton {
		t.Fatalf("↑ dari item pertama → %d, want tombol", m.bcursor)
	}
	if v := m.View(); !strings.Contains(v, "enter: hapus permanen seluruh isi Recycle Bin") {
		t.Errorf("penjelasan tombol tidak tampil:\n%s", v)
	}

	// Pada tombol, u dan space tidak melakukan apa pun.
	key(m, "u")
	key(m, " ")
	if len(m.bmarks) != 0 || m.popup != nil {
		t.Errorf("u/space pada tombol: tanda %v, popup %+v", m.bmarks, m.popup)
	}

	// enter: konfirmasi dulu; n membatalkan, y mengosongkan semuanya.
	key(m, "enter")
	if m.popup == nil || m.popup.title != "Kosongkan Recycle Bin?" {
		t.Fatalf("popup = %+v", m.popup)
	}
	key(m, "n")
	if emptied {
		t.Fatal("n tetap mengosongkan")
	}
	key(m, "enter")
	key(m, "y")
	if !emptied || len(purged) != 0 || len(m.binItems) != 0 || !strings.Contains(m.status, "2 item dihapus permanen") {
		t.Errorf("kosongkan: emptied %v, item per satu %v, sisa %d, status %q", emptied, purged, len(m.binItems), m.status)
	}
	if v := m.View(); !strings.Contains(v, "Recycle Bin kosong") || strings.Contains(v, "✕ Kosongkan") {
		t.Errorf("setelah kosong:\n%s", v)
	}

	// ↑ dari item pertama saat ada item lagi tetap bisa ke tombol; ↑ dari tombol wrap ke item terakhir.
	emptied = false
	run(m, m.loadBin())
	key(m, "g")
	key(m, "up")
	if m.bcursor != len(m.binItems)-1 {
		t.Errorf("↑ dari tombol → %d, want item terakhir (%d)", m.bcursor, len(m.binItems)-1)
	}
}

func TestElevateKey(t *testing.T) {
	root := tempDir(t)
	m := newModel(sizer.New())
	run(m, loadDir(root))

	m.admin = true
	key(m, "A")
	if m.elevate || m.status == "" {
		t.Errorf("admin: elevate %v, status %q; want tidak restart dan ada pesan", m.elevate, m.status)
	}

	m.admin = false
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("A")})
	if !m.elevate || cmd == nil {
		t.Fatalf("A: elevate %v, cmd %v; want restart", m.elevate, cmd)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("A tidak menghentikan TUI")
	}
}

// Hasil yang lebih panjang dari layar: Tab memilih item pertama tanpa
// menggulir ringkasan dan judul kelompok keluar layar.
func TestAnalysisTabKeepsTop(t *testing.T) {
	root := tempDir(t)
	for i := range 25 {
		write(t, filepath.Join(root, "proj", fmt.Sprintf("dir%02d", i), "x.bin"), (2+i)<<20)
	}
	ai := &fakeAI{}
	m := newAIModel(t, ai)
	run(m, loadDir(root))
	settle(t, m)
	key(m, "a")
	key(m, "enter")
	an := m.an
	if an == nil || an.phase != anDone {
		t.Fatalf("analisis tidak selesai: %+v", an)
	}
	top := func(when string) {
		t.Helper()
		v := m.View()
		for _, want := range []string{"● Sebagian besar ruang dipakai dependensi", "? Perlu dicek dulu · 25 item"} {
			if !strings.Contains(v, want) {
				t.Errorf("%s: %q hilang dari layar:\n%s", when, want, v)
			}
		}
	}
	top("setelah jawaban")
	key(m, "tab")
	top("setelah tab")
	key(m, "down")
	top("setelah down")

	// Sudah digulir ke bawah untuk membaca, lalu Tab: yang dipilih item yang
	// terlihat, layar tidak melompat.
	key(m, "esc")
	key(m, "pgdown")
	m.View()
	off := an.textOff
	key(m, "tab")
	m.View()
	cur, _, _ := m.current(an)
	if an.textOff != off || cur.line < off || cur.line >= off+m.chatHeight() {
		t.Errorf("tab setelah pgdown: textOff %d -> %d, kursor di baris %d", off, an.textOff, cur.line)
	}
	// Kembali ke item pertama (g): ringkasan dan judul ikut terlihat lagi.
	key(m, "g")
	top("setelah g")
}
// Hasil analisis tampil seperti daftar explorer: judul kelompok berwarna
// dengan jumlah item, folder induk yang sama ditulis sekali, akhiran platform
// disembunyikan, ukuran rata kanan, dan penjelasan yang sama ditulis sekali.
func TestResultLayout(t *testing.T) {
	root := `C:\u\.vscode`
	mk := func(id int, rel string, size int64, a analyze.Action) analyze.Mention {
		return analyze.Mention{Item: analyze.Item{ID: id, Path: root + `\` + rel, IsDir: true, Size: size}, Action: a}
	}
	res := &analyze.Result{
		Text: "Folder ekstensi VS Code.\n\nAman dihapus:\n• [[1]] Versi lama.\n• [[2]] Versi lama.\n\nPerlu dicek dulu:\n• [[3]] Ekstensi terpasang.",
		Mentions: map[int]analyze.Mention{
			1: mk(1, `extensions\anthropic.claude-code-2.1.312-win32-x64`, 300<<20, analyze.ActionDelete),
			2: mk(2, `extensions\ms-toolsai.jupyter-2025.7.0`, 17<<20, analyze.ActionDelete),
			3: mk(3, `extensions\openai.chatgpt-26.1`, 1500<<20, analyze.ActionReview),
		},
	}
	an := &analysis{root: root, exp: map[string]*expansion{}, marks: map[string]bool{}, gone: map[string]bool{}}
	m := newModel(sizer.New())
	var lines []string
	var stops []stop
	m.layoutAnswer(an, 1, res, 100, &lines, &stops)
	plain := make([]string, len(lines))
	for i, l := range lines {
		plain[i] = ansi.Strip(l)
	}
	out := strings.Join(plain, "\n")

	for _, want := range []string{"● Folder ekstensi VS Code.", "✕ Aman dihapus · 2 item", "? Perlu dicek dulu · 1 item",
		"▸ anthropic.claude-code-2.1.312\\ ", "▸ ms-toolsai.jupyter-2025.7.0\\ "} {
		if !strings.Contains(out, want) {
			t.Errorf("tidak memuat %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "win32-x64") {
		t.Errorf("akhiran platform masih tampil:\n%s", out)
	}
	// Folder induk yang sama ditulis sekali di bawah judul (hanya kelompok ≥ 2 item).
	if strings.Count(out, "extensions\\\n") != 1 || strings.Contains(out, "▸ extensions\\anthropic") {
		t.Errorf("folder induk tidak ditulis sekali:\n%s", out)
	}
	// Penjelasan yang sama untuk butir berurutan ditulis sekali, di bawah item terakhir.
	if strings.Count(out, "Versi lama.") != 1 || strings.Index(out, "Versi lama.") < strings.Index(out, "jupyter") {
		t.Errorf("penjelasan sama tidak digabung:\n%s", out)
	}
	// Ukuran rata kanan: berakhir di kolom yang sama.
	var ends []int
	for _, s := range stops {
		ends = append(ends, ansi.StringWidth(strings.TrimRight(plain[s.line], " ")))
	}
	if len(ends) != 3 || ends[0] != ends[1] || ends[1] != ends[2] {
		t.Errorf("ukuran tidak sejajar: %v\n%s", ends, out)
	}
}
// Header hasil: ukuran folder, bar bagian aman/cek/simpan, dan persen di
// legenda, judul kelompok, dan tiap item.
func TestResultPercent(t *testing.T) {
	root := `C:\u\.vscode`
	mk := func(id int, rel string, size int64, a analyze.Action) analyze.Mention {
		return analyze.Mention{Item: analyze.Item{ID: id, Path: root + `\` + rel, IsDir: true, Size: size}, Action: a}
	}
	res := &analyze.Result{
		Text: "Ringkasan.\n\nAman dihapus:\n• [[1]] Cache.\n\nPerlu dicek dulu:\n• [[2]] Besar.",
		Mentions: map[int]analyze.Mention{
			1: mk(1, `a`, 300<<20, analyze.ActionDelete),
			2: mk(2, `b`, 1500<<20, analyze.ActionReview),
		},
	}
	an := &analysis{root: root, res: res, snap: &analyze.Snapshot{Root: root, Size: 2000 << 20, Files: 1234},
		exp: map[string]*expansion{}, marks: map[string]bool{}, gone: map[string]bool{}}

	head := ansi.Strip(strings.Join(an.summaryHeader(100), "\n"))
	for _, want := range []string{root, "2.0 GB", "1.234 file", "aman 300.0 MB (15%)", "cek 1.5 GB (75%)", "lainnya 200.0 MB (10%)"} {
		if !strings.Contains(head, want) {
			t.Errorf("header tidak memuat %q:\n%s", want, head)
		}
	}
	// Bar selalu selebar sama, apa pun pembagiannya.
	if bar := strings.Split(head, "\n")[1]; ansi.StringWidth(strings.TrimSpace(bar)) != 60 {
		t.Errorf("lebar bar = %d, want 60: %q", ansi.StringWidth(strings.TrimSpace(bar)), bar)
	}

	m := newModel(sizer.New())
	var lines []string
	var stops []stop
	m.layoutAnswer(an, 1, res, 100, &lines, &stops)
	out := ansi.Strip(strings.Join(lines, "\n"))
	for _, want := range []string{"Aman dihapus · 1 item · 300.0 MB · 15% dari folder", "Perlu dicek dulu · 1 item · 1.5 GB · 75% dari folder",
		"300.0 MB  15%", "1.5 GB  75%"} {
		if !strings.Contains(out, want) {
			t.Errorf("tidak memuat %q:\n%s", want, out)
		}
	}
}
// "File baru" punya halaman sendiri dan rentang yang bisa dipilih: 24 jam
// (bawaan) atau 7 hari. Berganti rentang hanya menyaring, tanpa memindai ulang.
func TestRecentRange(t *testing.T) {
	now := time.Now()
	m := newModel(sizer.New())
	m.dirExists = func(string) bool { return true }
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	rep := &recent.Report{Since: now.Add(-recent.Window), Drives: []recent.DriveReport{{Root: `C:\`, Files: []recent.File{
		{Path: `C:\proj\baru.bin`, Size: 2 << 20, Created: now.Add(-2 * time.Hour)},
		{Path: `C:\unduh\lama.iso`, Size: 3 << 20, Created: now.Add(-3 * 24 * time.Hour)},
	}}}}
	m.Update(recentMsg{rep: rep})

	// Halaman Drive hanya berisi daftar drive; tab membuka halaman File baru.
	if v := m.View(); strings.Contains(v, "baru.bin") || !strings.Contains(v, "File baru") {
		t.Errorf("halaman Drive:\n%s", v)
	}
	key(m, "tab")
	if m.page != pageRecent || !m.focusRecent || m.rrange != rangeDay {
		t.Fatalf("tab: halaman %d, fokus %v, rentang %v", m.page, m.focusRecent, m.rrange)
	}

	// Bawaan 24 jam: hanya file 2 jam lalu.
	v := m.View()
	for _, want := range []string{"24 jam", "7 hari", "1 file", "baru.bin"} {
		if !strings.Contains(v, want) {
			t.Errorf("24 jam: View tidak memuat %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "lama.iso") {
		t.Errorf("file 3 hari lalu tampil di rentang 24 jam:\n%s", v)
	}
	if _, ok := m.newFiles().files[`C:\unduh\lama.iso`]; ok {
		t.Error("penanda explorer memuat file di luar rentang")
	}

	// t memilih 7 hari: kedua file tampil, yang lama dengan umur "3 hari lalu".
	key(m, "t")
	v = m.View()
	for _, want := range []string{"2 file", "baru.bin", "lama.iso", "3 hari lalu"} {
		if !strings.Contains(v, want) {
			t.Errorf("7 hari: View tidak memuat %q:\n%s", want, v)
		}
	}
	if m.rrange != rangeWeek || m.recentRunning {
		t.Errorf("rentang %v, memindai ulang %v", m.rrange, m.recentRunning)
	}
	if _, ok := m.newFiles().files[`C:\unduh\lama.iso`]; !ok {
		t.Error("penanda explorer tidak mengikuti rentang 7 hari")
	}
	if got := clockTime(now.Add(-3 * 24 * time.Hour)); got != "3 hari lalu" {
		t.Errorf("clockTime 3 hari = %q", got)
	}

	// t lagi, serta 1 dan 7, memilih langsung.
	key(m, "t")
	if m.rrange != rangeDay {
		t.Errorf("t kedua: rentang %v", m.rrange)
	}
	key(m, "7")
	key(m, "1")
	if m.rrange != rangeDay || len(m.recentFiles()) != 1 {
		t.Errorf("tombol 7 lalu 1: rentang %v, %d file", m.rrange, len(m.recentFiles()))
	}

	// Hasil yang hanya memuat 24 jam (dari versi lama) tidak cukup untuk 7 hari:
	// aplikasi memberi tahu dan memindai ulang.
	m.recent.Since = now.Add(-rangeDay)
	key(m, "7")
	if !strings.Contains(m.status, "belum lengkap") {
		t.Errorf("status = %q, want pemberitahuan data 7 hari belum lengkap", m.status)
	}
}