package recent

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Windows melaporkan sebuah file dengan ejaan path yang dipakai proses saat
// mengubahnya: nama panjang (C:\Users\T. Mustaqim\…) atau nama pendek 8.3
// (C:\Users\TA099~1.MUS\…, misalnya dari variabel TEMP). File yang dibuat
// lewat satu ejaan dan dihapus lewat ejaan lain akan dilaporkan sebagai dua
// path berbeda, jadi path selalu diseragamkan ke ejaan panjang (yang juga
// dipakai pemindaian awal) sebelum disimpan atau dibandingkan.

// Normalizer mengubah path ke ejaan panjang. Hasil per folder disimpan agar
// ribuan path di folder yang sama hanya memanggil Windows sekali.
type Normalizer struct {
	dirs map[string]string
}

// NewNormalizer membuat Normalizer kosong; pakai satu per batch path.
func NewNormalizer() *Normalizer { return &Normalizer{dirs: make(map[string]string)} }

// LongPath mengubah satu path ke ejaan panjang.
func LongPath(p string) string { return NewNormalizer().Path(p) }

// Path mengubah p ke ejaan panjang. Path yang sudah dihapus pun bisa
// diseragamkan: bagian yang masih ada diubah oleh Windows, sisanya dipakai apa
// adanya (namanya sudah ejaan yang dipakai saat dihapus).
func (n *Normalizer) Path(p string) string {
	p = filepath.Clean(p)
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(n.dir(parent), filepath.Base(p))
}

func (n *Normalizer) dir(d string) string {
	if v, ok := n.dirs[d]; ok {
		return v
	}
	out := d
	if long, ok := longName(d); ok {
		out = long
	} else if parent := filepath.Dir(d); parent != d {
		// Folder ini sudah tidak ada: seragamkan induknya yang masih ada.
		out = filepath.Join(n.dir(parent), filepath.Base(d))
	}
	n.dirs[d] = out
	return out
}

// longName meminta Windows mengubah path yang ada ke ejaan panjang.
func longName(p string) (string, bool) {
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, windows.MAX_PATH)
	for range 2 {
		n, err := windows.GetLongPathName(u, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return "", false
		}
		if int(n) <= len(buf) {
			return windows.UTF16ToString(buf[:n]), true
		}
		buf = make([]uint16, n) // path lebih panjang dari MAX_PATH
	}
	return "", false
}

// PruneMissing membuang file yang sudah tidak ada di disk, misalnya yang
// dihapus selagi program tertutup atau yang hanya sebentar ada (file
// sementara). Mengembalikan true jika ada yang dibuang. Memanggil Windows
// sekali per file, jadi panggil di luar goroutine UI.
func (r *Report) PruneMissing() bool {
	changed := false
	for i := range r.Drives {
		d := &r.Drives[i]
		kept := d.Files[:0]
		for _, f := range d.Files {
			if _, err := os.Lstat(f.Path); os.IsNotExist(err) {
				d.FileSize -= f.Size
				changed = true
				continue
			}
			kept = append(kept, f)
		}
		d.Files, d.idx = kept, nil
	}
	return changed
}

// Normalize menyeragamkan semua path di laporan ke ejaan panjang dan membuang
// duplikat yang timbul karena satu file tercatat dengan dua ejaan (misalnya
// dari hasil tersimpan sebelum perbaikan ini). Mengembalikan true jika ada yang berubah.
func (r *Report) Normalize() bool {
	n := NewNormalizer()
	changed := false
	for i := range r.Drives {
		d := &r.Drives[i]
		seen := make(map[string]bool, len(d.Files))
		kept := d.Files[:0]
		for _, f := range d.Files {
			if p := n.Path(f.Path); p != f.Path {
				f.Path, changed = p, true
			}
			if seen[f.Path] {
				d.FileSize -= f.Size
				changed = true
				continue
			}
			seen[f.Path] = true
			kept = append(kept, f)
		}
		d.Files, d.idx = kept, nil
	}
	return changed
}
