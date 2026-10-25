package analyze

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestBuild(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "web", "node_modules", "pkg", "index.js"), 5000)
	write(t, filepath.Join(root, "web", "src", "main.go"), 2000)
	write(t, filepath.Join(root, "videos", "raw.mp4"), 8000)
	write(t, filepath.Join(root, "videos", "tiny.txt"), 10)
	write(t, filepath.Join(root, "notes.txt"), 10)

	s, err := Build(context.Background(), root, Limits{MaxLines: 100, MinSize: 1000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.Size != 15020 || s.Files != 5 {
		t.Errorf("Size %d Files %d, want 15020 dan 5", s.Size, s.Files)
	}
	t.Log("\n" + s.Text)

	for _, want := range []string{
		"node_modules\\  4.9 KB (1 file, 1 folder, tidak dirinci)",
		"raw.mp4  7.8 KB, diubah",
		"… 1 item kecil lainnya, 10 B", // tiny.txt di videos, notes.txt di root
	} {
		if !strings.Contains(s.Text, want) {
			t.Errorf("ringkasan tidak memuat %q", want)
		}
	}
	// Isi node_modules tidak dirinci.
	if strings.Contains(s.Text, "pkg") || strings.Contains(s.Text, "index.js") {
		t.Error("isi node_modules ikut dirinci")
	}
	// Setiap id merujuk path yang ada.
	for id, it := range s.Items {
		if _, err := os.Stat(it.Path); err != nil {
			t.Errorf("[%d] %s tidak ada", id, it.Path)
		}
	}
}

func TestBuildBudget(t *testing.T) {
	root := t.TempDir()
	// Satu folder besar dengan banyak subfolder, satu folder kecil.
	for i := range 20 {
		write(t, filepath.Join(root, "big", string(rune('a'+i)), "f.bin"), 2000+i)
	}
	write(t, filepath.Join(root, "small", "x", "f.bin"), 1500)

	// Jatah 6 baris: root (1) + big, small (2) masih muat; merinci big
	// (20 baris) tidak muat, merinci small (1 baris) masih muat.
	s, err := Build(context.Background(), root, Limits{MaxLines: 6, MinSize: 1000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + s.Text)
	if s.Lines > 6 {
		t.Errorf("Lines = %d, melebihi batas 6", s.Lines)
	}
	if !strings.Contains(s.Text, "x\\") {
		t.Error("folder kecil yang masih muat tidak dirinci")
	}
	if strings.Contains(s.Text, "] a\\") {
		t.Error("folder big dirinci padahal melebihi jatah")
	}
}
