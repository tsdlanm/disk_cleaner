package sizer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func waitDone(t *testing.T, s *Sizer, path string) Info {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if inf, st := s.Get(path); st == Done {
			return inf
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s tidak selesai dihitung", path)
	return Info{}
}

func TestSizer(t *testing.T) {
	root := t.TempDir()
	big := filepath.Join(root, "big")
	nested := filepath.Join(big, "nested")
	write(t, filepath.Join(big, "a.bin"), 3000)
	write(t, filepath.Join(nested, "deep", "b.bin"), 2000)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := New()
	if _, st := s.Get(big); st != Unknown {
		t.Fatalf("state awal = %v, want Unknown", st)
	}
	s.Request(big, filepath.Join(root, "empty"))

	inf := waitDone(t, s, big)
	if inf.Size != 5000 || inf.Files != 2 || inf.Dirs != 2 {
		t.Errorf("big = %+v, want Size 5000, Files 2, Dirs 2", inf)
	}
	if inf := waitDone(t, s, filepath.Join(root, "empty")); inf.Size != 0 {
		t.Errorf("empty = %+v, want Size 0", inf)
	}

	// Folder turunan ikut tersimpan di cache tanpa diminta.
	if inf, st := s.Get(nested); st != Done || inf.Size != 2000 {
		t.Errorf("nested = %+v, %v; want 2000, Done", inf, st)
	}

	// Invalidate menghapus folder itu, turunannya, dan induknya.
	write(t, filepath.Join(nested, "c.bin"), 1000)
	s.Invalidate(nested)
	if _, st := s.Get(nested); st != Unknown {
		t.Errorf("nested setelah Invalidate = %v, want Unknown", st)
	}
	if _, st := s.Get(big); st != Unknown {
		t.Errorf("big (induk) setelah Invalidate = %v, want Unknown", st)
	}
	s.Request(big)
	if inf := waitDone(t, s, big); inf.Size != 6000 {
		t.Errorf("big setelah refresh = %d, want 6000", inf.Size)
	}
}
