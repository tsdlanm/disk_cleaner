package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsdlanm/cli_app/internal/analyze"
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
	root := t.TempDir()
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

	// Urut nama: folder dulu, lalu file.
	key(m, "s")
	if got, want := strings.Join(names(m), ","), "big,small,file.txt"; got != want {
		t.Errorf("urut nama = %s, want %s", got, want)
	}
	key(m, "s")

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

// fakeAI menyarankan hapus untuk node_modules dan simpan untuk folder yang
// dianalisis (id 1), dengan format jawaban yang sama seperti AI sungguhan.
type fakeAI struct{ calls int }

func (f *fakeAI) Complete(_ context.Context, _, user string, _ map[string]any) (string, error) {
	f.calls++
	m := regexp.MustCompile(`\[(\d+)\] node_modules`).FindStringSubmatch(user)
	if m == nil {
		return `{"answer":"Tidak ada yang perlu dihapus.","mentions":[]}`, nil
	}
	return `{"answer":"Sebagian besar ruang dipakai dependensi.\n\nAman dihapus:\n• [[` + m[1] +
		`]] Dependensi npm, bisa diinstal ulang.\n\nSebaiknya disimpan:\n• [[1]] Folder proyeknya sendiri.",` +
		`"mentions":[{"id":` + m[1] + `,"action":"hapus"},{"id":1,"action":"simpan"}]}`, nil
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

func TestAnalysis(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "web", "node_modules", "pkg", "x.js"), 5<<20)
	write(t, filepath.Join(root, "photos", "a.jpg"), 3<<20)

	ai := &fakeAI{}
	m := newAIModel(t, ai)
	run(m, loadDir(root))
	settle(t, m)

	// Kursor di folder web (terbesar): yang dianalisis folder web.
	key(m, "a")
	an := m.an
	if an == nil || an.root != filepath.Join(root, "web") {
		t.Fatalf("analisis = %+v, want root web", an)
	}
	if an.phase != anDone || ai.calls != 1 {
		t.Fatalf("phase %v (err %v), calls %d", an.phase, an.err, ai.calls)
	}
	// Dua penyebutan, sesuai urutan di jawaban: node_modules lalu folder web.
	nm, stops, _ := m.current(an)
	if len(stops) != 2 || stops[1].path != an.root {
		t.Fatalf("stops = %+v", stops)
	}
	if filepath.Base(nm.path) != "node_modules" || !nm.mention || an.res.Mentions[2].Action != analyze.ActionDelete {
		t.Fatalf("penyebutan pertama = %+v", nm)
	}
	v := m.View()
	for _, want := range []string{"Aman dihapus:", "• ▸ [node_modules\\ · 5.0 MB] Dependensi npm", "Sebaiknya disimpan:"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}

	// ↓ berpindah antar penyebutan dan berputar.
	key(m, "down")
	if sel, _, _ := m.current(an); sel.path != an.root {
		t.Errorf("down: terpilih %s, want folder web", sel.path)
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
	if v := m.View(); !strings.Contains(v, "[node_modules\\ · terhapus]") {
		t.Errorf("View tidak menandai terhapus:\n%s", v)
	}

	// esc kembali ke explorer, b membuka hasil lagi.
	key(m, "esc")
	if m.an != nil {
		t.Fatal("esc tidak menutup layar analisis")
	}
	key(m, "b")
	if m.an != an {
		t.Error("b tidak membuka hasil terakhir")
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
	root := t.TempDir()
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
	// Key apa pun diterima apa adanya; enter menyimpan lalu langsung menganalisis.
	run(m, typeText(m, "x"))
	if m.settings != nil || m.an == nil || m.an.phase != anDone || m.cfg.APIKey != "x" {
		t.Fatalf("setelah enter: settings %v, an %+v, key %q", m.settings, m.an, m.cfg.APIKey)
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
	root := t.TempDir()
	nm := filepath.Join(root, "proj", "node_modules")
	write(t, filepath.Join(nm, "react", "lib", "index.js"), 2<<20)
	write(t, filepath.Join(nm, "lodash", "lodash.js"), 1<<20)

	m := newAIModel(t, &fakeAI{})
	run(m, loadDir(root))
	settle(t, m)
	key(m, "a") // menganalisis folder proj
	an := m.an

	// enter pada node_modules membuka isinya di bawah path itu, di dalam chat.
	key(m, "enter")
	settle(t, m)
	if m.an != an || an.exp[nm] == nil {
		t.Fatalf("enter: an %v, exp %v", m.an, an.exp)
	}
	v := m.View()
	for _, want := range []string{"▾ [node_modules\\", "├ ▸ [react\\", "└ ▸ [lodash\\"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}

	// ↓ masuk ke isi folder yang terbuka, lalu enter membuka subfolder.
	key(m, "down")
	cur, stops, _ := m.current(an)
	if cur.mention || cur.parent != 0 || filepath.Base(cur.path) != "react" {
		t.Fatalf("down: stop %+v", cur)
	}
	key(m, "enter")
	settle(t, m)
	if !strings.Contains(m.View(), "[lib\\") {
		t.Errorf("subfolder react tidak terbuka:\n%s", m.View())
	}
	if n := len(m.answerStops(an)); n <= len(stops) {
		t.Errorf("stop tidak bertambah setelah membuka subfolder: %d", n)
	}

	// ← kembali ke folder induk (node_modules), ← lagi menutupnya.
	key(m, "left")
	if cur, _, _ := m.current(an); cur.key != "m:0" {
		t.Errorf("left: kursor di %s, want m:0", cur.key)
	}
	key(m, "left")
	if an.exp[nm] != nil {
		t.Error("left kedua tidak menutup node_modules")
	}

	// e membuka folder di explorer utama; b kembali ke chat.
	key(m, "e")
	if m.an != nil || m.path != nm {
		t.Errorf("e: an %v, path %s", m.an, m.path)
	}
	key(m, "b")
	if m.an != an {
		t.Error("b tidak kembali ke hasil analisis")
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
	root := t.TempDir()
	write(t, filepath.Join(root, "proj", "node_modules", "x.js"), 3<<20)
	write(t, filepath.Join(root, "proj", "big.iso"), 2<<20)

	bad := &badKeyAI{}
	m := newAIModel(t, bad)
	run(m, loadDir(root))
	settle(t, m)
	key(m, "a")
	an := m.an
	if an.phase != anFailed || !strings.Contains(m.View(), "Tekan p") {
		t.Fatalf("phase %v, view:\n%s", an.phase, m.View())
	}

	// v: data folder bisa dipilih walaupun AI gagal.
	key(m, "v")
	if !an.showData {
		t.Fatal("v tidak membuka data")
	}
	first, _, _ := m.current(an)
	key(m, "down")
	second, _, _ := m.current(an)
	if first.path == second.path || second.path == "" {
		t.Errorf("down di data tidak berpindah: %s -> %s", first.path, second.path)
	}
	// Pilih node_modules lalu enter: dibuka di explorer utama, b kembali.
	for range len(an.dataStops()) {
		if cur, _, _ := m.current(an); filepath.Base(cur.path) == "node_modules" {
			break
		}
		key(m, "down")
	}
	key(m, "enter")
	if m.an != nil || filepath.Base(m.path) != "node_modules" {
		t.Fatalf("enter di data: an %v, path %s", m.an, m.path)
	}
	key(m, "b")

	// p di layar analisis: ganti API key, esc simpan, r analisis ulang.
	key(m, "p")
	if m.settings == nil {
		t.Fatal("p tidak membuka pengaturan")
	}
	key(m, "down")
	key(m, "down")
	key(m, "enter")
	run(m, typeText(m, "kunci-baru"))
	key(m, "esc")
	key(m, "r")
	if bad.calls != 2 || m.cfg.APIKey != "kunci-baru" {
		t.Errorf("calls %d, key %q", bad.calls, m.cfg.APIKey)
	}
}

func TestCursorWrap(t *testing.T) {
	root := t.TempDir()
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
	root := t.TempDir()
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

// recentReport membuat laporan dengan n folder berisi file baru di setiap
// drive; file di folder ke-i dibuat i jam lalu.
func recentReport(n int, roots ...string) *recent.Report {
	now := time.Now()
	rep := &recent.Report{}
	for _, root := range roots {
		d := recent.DriveReport{Root: root, SmallCount: 7, SmallSize: 70}
		for i := range n {
			d.Files = append(d.Files, recent.File{
				Path:    filepath.Join(root, fmt.Sprintf("proj%02d", i), "f.bin"),
				Size:    int64(i+1) << 20,
				Created: now.Add(-time.Duration(i) * time.Hour),
			})
			d.FileSize += int64(i+1) << 20
		}
		rep.Drives = append(rep.Drives, d)
	}
	return rep
}

func TestRecent(t *testing.T) {
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	m.Update(recentMsg{rep: recentReport(25, `C:\`, `D:\`)})

	v := m.View()
	for _, want := range []string{"File baru", `C:\ · 25 folder · 25 file baru`, `D:\ · 25 folder`, "proj00", "proj04", "▸ tampilkan 5 lagi (20 folder tersisa)"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	// Per drive hanya 5 folder terbaru: proj05 belum tampil.
	if strings.Contains(v, "proj05") {
		t.Error("lebih dari 5 folder per drive")
	}

	// tab fokus ke daftar; turun ke "tampilkan 5 lagi" di drive C (posisi ke-6).
	key(m, "tab")
	for range 5 {
		key(m, "down")
	}
	key(m, "enter")
	if m.shownFor(`C:\`) != 10 || m.shownFor(`D:\`) != 5 {
		t.Errorf("tampil C %d, D %d; want 10 dan 5", m.shownFor(`C:\`), m.shownFor(`D:\`))
	}
	if v := m.View(); !strings.Contains(v, "proj09") || !strings.Contains(v, "(15 folder tersisa)") {
		t.Errorf("setelah tampilkan lagi:\n%s", v)
	}
}

// Beberapa file di folder yang sama menjadi satu baris folder; subfolder
// bernama acak/cache digabung ke induknya; cache dipisah dari file pengguna.
func TestRecentGroups(t *testing.T) {
	now := time.Now()
	at := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.Update(recentMsg{rep: &recent.Report{Drives: []recent.DriveReport{{Root: `C:\`, Files: []recent.File{
		{Path: `C:\go-build\1b\a`, Size: 1 << 20, Created: at(1), Kind: recent.KindCache},
		{Path: `C:\dl\a.iso`, Size: 3 << 20, Created: at(2)},
		{Path: `C:\go-build\ec\b`, Size: 1 << 20, Created: at(3), Kind: recent.KindCache},
		{Path: `C:\Chrome\Profile 19\Cache\Cache_Data\f_01`, Size: 1 << 20, Created: at(4), Kind: recent.KindCache},
		{Path: `C:\x\88809242bdf24896832995bb29dc7440\b.bin`, Size: 1 << 20, Created: at(5)},
		{Path: `C:\dl\c.zip`, Size: 2 << 20, Created: at(6)},
		{Path: `C:\lama\d.bin`, Size: 1 << 20, Created: now.AddDate(0, 0, -20)}, // di luar rentang
	}}}}})
	g := m.groups(0)
	dirs := func(gs []folderGroup) string {
		var out []string
		for _, x := range gs {
			out = append(out, fmt.Sprintf("%s:%d", x.dir, x.count))
		}
		return strings.Join(out, ",")
	}
	if got := dirs(g.user); got != `C:\dl:2,C:\x:1` {
		t.Errorf("folder pengguna = %s", got)
	}
	if got := dirs(g.other); got != `C:\go-build:2,C:\Chrome\Profile 19:1` || g.otherFiles != 3 {
		t.Errorf("folder cache = %s (%d file)", got, g.otherFiles)
	}

	// Cache tertutup dulu sebagai satu ringkasan; enter membukanya.
	v := m.View()
	for _, want := range []string{`C:\ · 2 folder · 3 file baru`, "▸ cache, aplikasi & sistem: 2 folder · 3 file"} {
		if !strings.Contains(v, want) {
			t.Errorf("View tidak memuat %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "go-build") {
		t.Errorf("cache tampil sebelum dibuka:\n%s", v)
	}
	key(m, "tab")
	key(m, "down")
	key(m, "down")
	key(m, "enter")
	if v := m.View(); !strings.Contains(v, "▾ cache") || !strings.Contains(v, "go-build") || !strings.Contains(v, "Profile 19") {
		t.Errorf("setelah dibuka:\n%s", v)
	}
}

func TestGroupDir(t *testing.T) {
	for dir, want := range map[string]string{
		`C:\go-build\1b`:                                         `C:\go-build`,
		`C:\Chrome\Profile 19\Cache\Cache_Data`:                  `C:\Chrome\Profile 19`,
		`D:\.gradle\transforms\37ce2ac697794a81cb8c\transformed`: `D:\.gradle\transforms`,
		`D:\foto\2024\Liburan`:                                   `D:\foto\2024\Liburan`,
		`C:\Users\a\Downloads`:                                   `C:\Users\a\Downloads`,
	} {
		if got := groupDir(dir); got != want {
			t.Errorf("groupDir(%s) = %s, want %s", dir, got, want)
		}
	}
}

func TestNoisyName(t *testing.T) {
	for name, want := range map[string]bool{
		"1b": true, "ec": true, "Cache_Data": true, "88809242bdf24896832995bb29dc7440": true,
		"{3F2504E0-4F89-11D3-9A0C-0305E82C3301}": true, "37ce2ac697794a81": true,
		"Downloads": false, "dl": false, "cafe": false, "Profile 19": false, "proj01": false, "2024": false, "d00": false, "20241026": false,
	} {
		if got := noisyName(name); got != want {
			t.Errorf("noisyName(%q) = %v, want %v", name, got, want)
		}
	}
	if got := truncateMiddle("88809242bdf24896832995bb29dc7440", 12); got != "88809…dc7440" {
		t.Errorf("truncateMiddle = %q, want 88809…dc7440", got)
	}
}
func TestSnapshotStartup(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, "baru.iso"), 2<<20)
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
	if v := m.View(); !strings.Contains(v, "dipindai 10 menit lalu") || !strings.Contains(v, "cache, aplikasi & sistem: 1 folder · 1 file") {
		t.Errorf("View:\n%s", v)
	}

	// Hasil tersimpan yang sudah lama: dipindai ulang, lalu disimpan lagi.
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
	root := t.TempDir()
	dl := filepath.Join(root, "dl")
	iso := filepath.Join(dl, "big.iso")
	write(t, iso, 100)

	m := newModel(sizer.New())
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m.cfg.RecentMinKB = 0 // file 100 byte tetap dihitung "baru" saat diperiksa ulang
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
	// Folder tes ada di Temp, jadi berlabel cache: buka bagian cache dulu.
	m.rexpand = map[string]bool{filepath.VolumeName(root) + `\`: true}
	if v := m.View(); !strings.Contains(v, "1 file") || !strings.Contains(v, "3 jam lalu") {
		t.Errorf("View:\n%s", v)
	}

	// enter membuka folder itu.
	key(m, "tab")
	key(m, "down")
	key(m, "enter")
	if m.path != dl {
		t.Fatalf("enter: path %s, want %s", m.path, dl)
	}

	// File dihapus dari explorer: langsung hilang dari "File baru".
	key(m, "d")
	key(m, "y")
	if g := m.groups(0); len(g.user)+len(g.other) != 0 {
		t.Errorf("folder masih ada di File baru: %+v", g)
	}
}
func TestElevateKey(t *testing.T) {
	root := t.TempDir()
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
