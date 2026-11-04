// Package trend mencatat ukuran folder terbesar dari waktu ke waktu, untuk
// menjawab "folder mana yang bertambah" tanpa menyimpan daftar file.
package trend

import (
	"cmp"
	"encoding/gob"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	// MinDir: folder lebih kecil dari ini tidak dicatat.
	MinDir = 50 << 20
	// MaxDirs: paling banyak folder terbesar yang dicatat per titik waktu.
	MaxDirs = 600
	// Keep: titik waktu lebih tua dari ini dibuang.
	Keep = 40 * 24 * time.Hour
	// minGap: jarak minimum antar titik waktu.
	minGap = 3 * time.Hour
	// MinDelta: pertambahan lebih kecil dari ini tidak ditampilkan.
	MinDelta = 10 << 20
)

// Point adalah ukuran folder-folder terbesar pada satu waktu.
type Point struct {
	Time time.Time
	Dirs map[string]int64
}

// History adalah rangkaian titik waktu, dari yang terlama.
type History struct {
	Points []Point
}

// Path mengembalikan lokasi file riwayat di dalam baseDir.
func Path(baseDir string) string { return filepath.Join(baseDir, "trend.gob") }

// Load membaca riwayat; riwayat kosong jika belum ada atau rusak.
func Load(path string) *History {
	h := &History{}
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	if err := gob.NewDecoder(f).Decode(h); err != nil {
		return &History{}
	}
	return h
}

// Save menyimpan riwayat (ditulis ke file sementara lalu diganti).
func (h *History) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(h); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add mencatat ukuran folder pada waktu t. Hanya folder terbesar yang
// disimpan. Titik yang terlalu dekat dengan titik terakhir diabaikan (kecuali
// lebih baru, maka menggantikannya). Titik lebih tua dari 2 hari dijarangkan
// jadi satu per hari, dan yang lebih tua dari Keep dibuang. Mengembalikan
// true jika riwayat berubah.
func (h *History) Add(t time.Time, sizes map[string]int64) bool {
	dirs := top(sizes)
	if len(dirs) == 0 {
		return false
	}
	if n := len(h.Points); n > 0 {
		last := &h.Points[n-1]
		switch {
		case !t.After(last.Time):
			return false
		case t.Sub(last.Time) < minGap:
			*last = Point{Time: t, Dirs: dirs}
			return true
		}
	}
	h.Points = append(h.Points, Point{Time: t, Dirs: dirs})
	h.thin(t)
	return true
}

// top memilih paling banyak MaxDirs folder terbesar yang ≥ MinDir.
func top(sizes map[string]int64) map[string]int64 {
	type kv struct {
		p string
		s int64
	}
	var l []kv
	for p, s := range sizes {
		if s >= MinDir && filepath.Dir(p) != p { // akar drive tidak dicatat
			l = append(l, kv{p, s})
		}
	}
	slices.SortFunc(l, func(a, b kv) int { return cmp.Compare(b.s, a.s) })
	if len(l) > MaxDirs {
		l = l[:MaxDirs]
	}
	out := make(map[string]int64, len(l))
	for _, e := range l {
		out[e.p] = e.s
	}
	return out
}

func (h *History) thin(now time.Time) {
	var out []Point
	for _, p := range h.Points {
		if now.Sub(p.Time) > Keep {
			continue
		}
		if n := len(out); n > 0 && now.Sub(p.Time) > 48*time.Hour && now.Sub(out[n-1].Time) > 48*time.Hour &&
			sameDay(out[n-1].Time, p.Time) {
			out[n-1] = p // satu titik per hari: yang terakhir
			continue
		}
		out = append(out, p)
	}
	h.Points = out
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// Base memilih titik pembanding untuk rentang d terhadap titik terakhir:
// titik yang waktunya paling dekat dengan (terakhir - d), asalkan umurnya
// antara setengah dan dua kali d. ok false jika riwayat belum cukup panjang.
func (h *History) Base(d time.Duration) (cur, base Point, ok bool) {
	n := len(h.Points)
	if n < 2 {
		return Point{}, Point{}, false
	}
	cur = h.Points[n-1]
	want := cur.Time.Add(-d)
	best := -1
	for i := range n - 1 {
		p := h.Points[i]
		if age := cur.Time.Sub(p.Time); age < d/2 || age > 2*d {
			continue
		}
		if best < 0 || abs(p.Time.Sub(want)) < abs(h.Points[best].Time.Sub(want)) {
			best = i
		}
	}
	if best < 0 {
		return Point{}, Point{}, false
	}
	return cur, h.Points[best], true
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// Growth adalah pertambahan ukuran satu folder.
type Growth struct {
	Path  string
	Delta int64 // pertambahan
	Size  int64 // ukuran sekarang
}

// Compare mengembalikan folder yang paling bertambah dari base ke cur, paling
// banyak limit. Folder yang tidak tercatat di base dianggap sebesar ambang
// pencatatan, jadi hanya kelebihannya yang dihitung. Folder induk yang
// pertambahannya hampir seluruhnya dari satu subfolder tidak ditampilkan
// (subfolder itulah yang tampil), dan subfolder dari folder yang sudah
// ditampilkan dilewati agar tidak dihitung dua kali.
func Compare(base, cur Point, limit int) []Growth {
	floor := int64(MinDir)
	if len(base.Dirs) >= MaxDirs {
		floor = slices.Min(valuesOf(base.Dirs))
	}
	var c []Growth
	for p, s := range cur.Dirs {
		prev, ok := base.Dirs[p]
		if !ok {
			prev = floor
		}
		if d := s - prev; d >= MinDelta {
			c = append(c, Growth{Path: p, Delta: d, Size: s})
		}
	}
	slices.SortFunc(c, func(a, b Growth) int {
		if a.Delta != b.Delta {
			return cmp.Compare(b.Delta, a.Delta)
		}
		return strings.Compare(a.Path, b.Path)
	})
	var out []Growth
	for _, g := range c {
		if len(out) >= limit {
			break
		}
		explained := false
		for _, o := range c {
			if o.Path != g.Path && Under(o.Path, g.Path) && o.Delta*10 >= g.Delta*6 {
				explained = true
				break
			}
		}
		if explained {
			continue
		}
		covered := false
		for _, o := range out {
			if Under(g.Path, o.Path) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, g)
		}
	}
	return out
}

func valuesOf(m map[string]int64) []int64 {
	out := make([]int64, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// Under melaporkan apakah path berada di dalam dir (tidak sama dengan dir).
func Under(path, dir string) bool {
	dir = strings.TrimRight(dir, `\`) + `\`
	return len(path) > len(dir) && strings.EqualFold(path[:len(dir)], dir)
}
