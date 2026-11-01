package analyze

import (
	"os"
	"path/filepath"
	"testing"
)

// File konfigurasi lama yang masih memuat pengaturan yang sudah dihapus
// (recent_days) tetap terbaca; kuncinya diabaikan dan pengaturan lain utuh.
func TestLoadConfigIgnoresRemovedKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	if err := os.MkdirAll(filepath.Join(dir, "scan"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"provider":"gemini","api_key":"kunci-rahasia","recent_days":7,"recent_min_kb":2048}`
	if err := os.WriteFile(filepath.Join(dir, "scan", "config.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("config lama gagal dibaca: %v", err)
	}
	if cfg.APIKey != "kunci-rahasia" || cfg.RecentMinKB != 2048 {
		t.Errorf("pengaturan lain tidak utuh: %+v", cfg)
	}
}
