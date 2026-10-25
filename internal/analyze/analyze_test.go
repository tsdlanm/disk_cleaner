package analyze

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	s := &Snapshot{Items: map[int]Item{
		2: {ID: 2, Path: `C:\x\cache`, IsDir: true, Size: 60},
		3: {ID: 3, Path: `C:\x\big.iso`, Size: 30},
	}}
	raw := `{"answer":"Ringkasan.\n• [[2]] cache\n• [[3]] iso\n• [[99]] tidak ada","mentions":[
		{"id":2,"action":"hapus"},{"id":3,"action":"buang"}]}`
	res, err := Parse(raw, s)
	if err != nil {
		t.Fatal(err)
	}
	// Penanda id tidak dikenal dibuang dari teks.
	if strings.Contains(res.Text, "[[99]]") {
		t.Errorf("penanda id tidak dikenal masih ada: %q", res.Text)
	}
	if res.Mentions[2].Action != ActionDelete {
		t.Errorf("aksi [[2]] = %q, want hapus", res.Mentions[2].Action)
	}
	// Aksi tidak dikenal dianggap "cek".
	if res.Mentions[3].Action != ActionReview || res.Mentions[3].Path != `C:\x\big.iso` {
		t.Errorf("mention [[3]] = %+v", res.Mentions[3])
	}

	var ids []int
	var text strings.Builder
	for _, sg := range res.Segments() {
		if sg.ID > 0 {
			ids = append(ids, sg.ID)
		} else {
			text.WriteString(sg.Text)
		}
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 3 {
		t.Errorf("urutan penyebutan = %v, want [2 3]", ids)
	}
	if text.String() != "Ringkasan.\n•  cache\n•  iso\n•  tidak ada" {
		t.Errorf("teks = %q", text.String())
	}

	if _, err := Parse("bukan json", s); err == nil {
		t.Error("jawaban bukan JSON harus error")
	}
}

// Kasus nyata dari gemini-3.5-flash-lite: "mentions" terisi, tetapi teks
// jawaban tidak memuat penanda [[id]] sama sekali.
func TestParseMissingMarkers(t *testing.T) {
	s := &Snapshot{Items: map[int]Item{
		2:   {ID: 2, Path: `C:\p\Service Worker`, IsDir: true},
		334: {ID: 334, Path: `C:\p\Code Cache`, IsDir: true},
		433: {ID: 433, Path: `C:\p\History`},
	}}
	raw := `{"answer":"Sebagian besar ruang dipakai cache.","mentions":[
		{"id":2,"action":"hapus","reason":"Cache service worker."},
		{"id":334,"action":"hapus","reason":"Cache kode JavaScript."},
		{"id":433,"action":"cek","reason":"Riwayat penjelajahan."},
		{"id":999,"action":"hapus","reason":"id tidak ada"}]}`
	res, err := Parse(raw, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mentions) != 3 {
		t.Fatalf("mentions = %d, want 3", len(res.Mentions))
	}
	var ids []int
	for _, sg := range res.Segments() {
		if sg.ID > 0 {
			ids = append(ids, sg.ID)
		}
	}
	if len(ids) != 3 || ids[0] != 2 || ids[1] != 334 || ids[2] != 433 {
		t.Errorf("penanda di teks = %v, want [2 334 433]", ids)
	}
	for _, want := range []string{"Sebagian besar ruang dipakai cache.", "Aman dihapus:\n• [[2]] Cache service worker.", "Perlu dicek dulu:\n• [[433]] Riwayat penjelajahan."} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("teks tidak memuat %q:\n%s", want, res.Text)
		}
	}
}
