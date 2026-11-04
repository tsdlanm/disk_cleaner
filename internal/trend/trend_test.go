package trend

import (
	"path/filepath"
	"testing"
	"time"
)

const mb = 1 << 20

func TestAddThinsAndPrunes(t *testing.T) {
	now := time.Now()
	h := &History{}
	sz := map[string]int64{`C:\a`: 100 * mb, `C:\small`: mb, `C:\`: 900 * mb}
	y, mo, d := now.AddDate(0, 0, -10).Date()
	noon := time.Date(y, mo, d, 12, 0, 0, 0, now.Location()) // dua titik di hari kalender yang sama
	for _, at := range []time.Time{now.AddDate(0, 0, -60), noon.Add(-4 * time.Hour), noon.Add(4 * time.Hour), now.Add(-time.Hour)} {
		h.Add(at, sz)
	}
	if len(h.Points) != 2 { // 60 hari dibuang, dua titik sehari yang sama jadi satu
		t.Fatalf("points = %d, want 2", len(h.Points))
	}
	if _, ok := h.Points[0].Dirs[`C:\small`]; ok {
		t.Error("folder kecil ikut dicatat")
	}
	if _, ok := h.Points[0].Dirs[`C:\`]; ok {
		t.Error("akar drive ikut dicatat")
	}
	// Titik terlalu dekat menggantikan yang terakhir; yang lebih lama diabaikan.
	h.Add(now.Add(-time.Hour+time.Minute), map[string]int64{`C:\a`: 200 * mb})
	if len(h.Points) != 2 || h.Points[1].Dirs[`C:\a`] != 200*mb {
		t.Errorf("titik dekat tidak menggantikan: %+v", h.Points)
	}
	if h.Add(now.Add(-2*time.Hour), sz) {
		t.Error("titik lebih lama dari yang terakhir diterima")
	}
}

func TestBase(t *testing.T) {
	now := time.Now()
	h := &History{}
	if _, _, ok := h.Base(24 * time.Hour); ok {
		t.Error("riwayat kosong punya pembanding")
	}
	h.Add(now.Add(-6*time.Hour), map[string]int64{`C:\a`: 100 * mb})
	h.Add(now, map[string]int64{`C:\a`: 200 * mb})
	if _, _, ok := h.Base(24 * time.Hour); ok {
		t.Error("titik 6 jam dipakai untuk 24 jam")
	}
	h.Points = append([]Point{{Time: now.Add(-26 * time.Hour), Dirs: map[string]int64{`C:\a`: 50 * mb}}}, h.Points...)
	_, base, ok := h.Base(24 * time.Hour)
	if !ok || base.Dirs[`C:\a`] != 50*mb {
		t.Errorf("base 24 jam = %+v, %v", base, ok)
	}
	if _, _, ok := h.Base(30 * 24 * time.Hour); ok {
		t.Error("30 hari tanpa riwayat cukup")
	}
}

func TestCompare(t *testing.T) {
	old := Point{Dirs: map[string]int64{
		`C:\Users`:                  1000 * mb,
		`C:\Users\a`:                900 * mb,
		`C:\Users\a\AppData`:        800 * mb,
		`C:\Users\a\AppData\Docker`: 700 * mb,
		`C:\Other`:                  200 * mb,
		`C:\Same`:                   300 * mb,
	}}
	cur := Point{Dirs: map[string]int64{
		`C:\Users`:                  3000 * mb,
		`C:\Users\a`:                2900 * mb,
		`C:\Users\a\AppData`:        2800 * mb,
		`C:\Users\a\AppData\Docker`: 2700 * mb, // +2000 MB: penjelas utama
		`C:\Other`:                  600 * mb,  // +400 MB
		`C:\Same`:                   300 * mb,
		`C:\New`:                    450 * mb, // baru: 450 - 50 = +400 MB
	}}
	got := Compare(old, cur, 10)
	if len(got) != 3 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Path != `C:\Users\a\AppData\Docker` || got[0].Delta != 2000*mb {
		t.Errorf("pertama = %+v", got[0])
	}
	for _, g := range got {
		if g.Path == `C:\Users` || g.Path == `C:\Same` {
			t.Errorf("tidak seharusnya tampil: %+v", g)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	p := Path(t.TempDir())
	h := &History{}
	h.Add(time.Now(), map[string]int64{`C:\a`: 100 * mb})
	if err := h.Save(p); err != nil {
		t.Fatal(err)
	}
	if got := Load(p); len(got.Points) != 1 || got.Points[0].Dirs[`C:\a`] != 100*mb {
		t.Errorf("load = %+v", got)
	}
	if got := Load(filepath.Join(t.TempDir(), "tidak-ada")); len(got.Points) != 0 {
		t.Error("file tidak ada harus kosong")
	}
}
