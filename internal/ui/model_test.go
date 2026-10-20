package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	case nil, tickMsg, tea.QuitMsg:
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
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
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

	// Popup analisis AI untuk item yang dipilih; esc menutupnya tanpa keluar folder.
	key(m, "a")
	if m.popup == nil {
		t.Fatal("popup tidak terbuka setelah menekan a")
	}
	if v := m.View(); !strings.Contains(v, "Analisis AI") || !strings.Contains(v, "file.txt") {
		t.Errorf("View popup tidak memuat judul/nama item:\n%s", v)
	}
	key(m, "esc")
	if m.popup != nil || m.path != root {
		t.Errorf("esc: popup %v, path %s", m.popup, m.path)
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
