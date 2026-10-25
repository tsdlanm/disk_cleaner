package recent

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"time"

	"github.com/tsdlanm/cli_app/internal/sizer"
)

// Snapshot adalah hasil pemindaian yang disimpan ke disk, agar program bisa
// langsung menampilkannya saat dibuka tanpa memindai ulang seluruh drive.
type Snapshot struct {
	Time   time.Time // waktu pemindaian penuh
	Report *Report
	Dirs   map[string]sizer.Info // ukuran folder, untuk mengisi cache explorer
	// Synced adalah saat terakhir Report diperbarui dari pemantauan langsung
	// (lihat Report.Apply); sampai saat itu datanya masih lengkap.
	Synced time.Time
}

// Fresh mengembalikan waktu terakhir data snapshot masih lengkap.
func (s *Snapshot) Fresh() time.Time {
	if s.Synced.After(s.Time) {
		return s.Synced
	}
	return s.Time
}

// MinSnapshotDir: hanya folder sebesar ini yang ukurannya disimpan, agar file
// snapshot tetap kecil. Folder yang lebih kecil cepat dihitung ulang saat dibuka.
const MinSnapshotDir = 1 << 20

// SnapshotPath mengembalikan lokasi file snapshot di dalam baseDir.
func SnapshotPath(baseDir string) string {
	return filepath.Join(baseDir, "snapshot.gob")
}

// SaveSnapshot menyimpan s ke path (ditulis ke file sementara lalu diganti,
// agar file lama tidak rusak jika penyimpanan terputus).
func SaveSnapshot(path string, s *Snapshot) error {
	return writeGob(path, s)
}

// LoadSnapshot membaca snapshot dari path.
func LoadSnapshot(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s Snapshot
	if err := gob.NewDecoder(f).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

func writeGob(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(v); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
