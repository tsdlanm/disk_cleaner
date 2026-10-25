package watch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// wait menunggu sampai semua want muncul di batch dari w.
func wait(t *testing.T, w *Watcher, want ...string) {
	t.Helper()
	seen := map[string]bool{}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b, ok := <-w.C:
			if !ok {
				t.Fatal("C ditutup")
			}
			for _, p := range b.Paths {
				seen[p] = true
			}
			if !slices.ContainsFunc(want, func(p string) bool { return !seen[p] }) {
				return
			}
		case <-deadline:
			t.Fatalf("perubahan tidak dilaporkan: want %v, dapat %v", want, seen)
		}
	}
}

func TestWatch(t *testing.T) {
	Interval = 20 * time.Millisecond
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := Start([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// File baru di subfolder dalam ikut terpantau.
	f := filepath.Join(sub, "baru.bin")
	if err := os.WriteFile(f, make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	wait(t, w, f)

	// Ganti nama dan hapus: nama lama dan baru sama-sama dilaporkan.
	g := filepath.Join(root, "pindah.bin")
	if err := os.Rename(f, g); err != nil {
		t.Fatal(err)
	}
	wait(t, w, f, g)
	if err := os.Remove(g); err != nil {
		t.Fatal(err)
	}
	wait(t, w, g)

	w.Close()
	select {
	case _, ok := <-w.C:
		if ok {
			// Batch terakhir yang masih tertunda boleh ada; setelahnya harus tertutup.
			if _, ok := <-w.C; ok {
				t.Error("C belum ditutup setelah Close")
			}
		}
	case <-time.After(2 * time.Second):
		t.Error("C tidak ditutup setelah Close")
	}
}

func TestStartNoRoot(t *testing.T) {
	if _, err := Start([]string{filepath.Join(t.TempDir(), "tidak-ada")}); err == nil {
		t.Error("Start tanpa root yang valid tidak mengembalikan error")
	}
}
