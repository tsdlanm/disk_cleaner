package trash

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMove benar-benar memindahkan dua file kecil ke Recycle Bin pengguna,
// jadi hanya dijalankan jika diminta: SCAN_TEST_TRASH=1 go test ./internal/trash/
func TestMove(t *testing.T) {
	if os.Getenv("SCAN_TEST_TRASH") == "" {
		t.Skip("set SCAN_TEST_TRASH=1 untuk menguji Recycle Bin sungguhan")
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "scan_cli_test_a.txt")
	sub := filepath.Join(dir, "scan_cli_test_folder")
	if err := os.WriteFile(a, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sub, "x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Move([]string{a, sub}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{a, sub} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s masih ada setelah Move", p)
		}
	}
}
