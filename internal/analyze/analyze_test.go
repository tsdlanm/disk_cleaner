package analyze

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	s := &Snapshot{
		Items: map[int]Item{
			2: {ID: 2, Path: `C:\x\cache`, IsDir: true, Size: 60},
			3: {ID: 3, Path: `C:\x\big.iso`, Size: 30},
			4: {ID: 4, Path: `C:\x\npm-cache`, IsDir: true, Size: 90},
			5: {ID: 5, Path: `C:\x\docs`, IsDir: true, Size: 10},
			6: {ID: 6, Path: `C:\x\lain`, IsDir: true, Size: 5},
		},
		Candidates: []Candidate{{ID: 4}, {ID: 2}, {ID: 3}, {ID: 5}},
	}
	raw := `{"summary":"Folder kerja [[2]] berisi cache.","items":[
		{"id":2,"action":"hapus","reason":"Cache.\nDibuat ulang."},
		{"id":4,"action":"HAPUS","reason":"Cache npm; npm cache clean --force."},
		{"id":3,"action":"buang","reason":"Image ISO [[3]]."},
		{"id":6,"action":"hapus","reason":"tidak diminta"},
		{"id":99,"action":"hapus","reason":"id tidak ada"}]}`
	res, err := Parse(raw, s)
	if err != nil {
		t.Fatal(err)
	}
	// Kelompok disusun aplikasi, dari yang terbesar; aksi tak dikenal jadi "cek";
	// id yang tidak diminta diabaikan; kandidat yang tidak dinilai jadi "cek".
	want := "Folder kerja berisi cache.\n\n" +
		"Aman dihapus:\n• [[4]] Cache npm; npm cache clean --force.\n• [[2]] Cache. Dibuat ulang.\n\n" +
		"Perlu dicek dulu:\n• [[3]] Image ISO.\n• [[5]] " + noteMissing
	if res.Text != want {
		t.Errorf("teks:\n got %q\nwant %q", res.Text, want)
	}
	if len(res.Mentions) != 4 || res.Mentions[3].Action != ActionReview || res.Mentions[5].Reason != noteMissing {
		t.Errorf("mentions = %+v", res.Mentions)
	}
	if _, ok := res.Mentions[6]; ok {
		t.Error("id di luar daftar wajib ikut ditampilkan")
	}
	if _, err := Parse("bukan json", s); err == nil {
		t.Error("jawaban bukan JSON harus error")
	}
}

// Tanpa daftar kandidat (snapshot lama atau buatan tes), semua item boleh dinilai.
func TestParseWithoutCandidates(t *testing.T) {
	s := &Snapshot{Items: map[int]Item{2: {ID: 2, Path: `C:\x\cache`, IsDir: true}}}
	res, err := Parse(`{"summary":"Ringkas.","items":[{"id":2,"action":"simpan","reason":"Penting."}]}`, s)
	if err != nil || res.Text != "Ringkas.\n\nSebaiknya disimpan:\n• [[2]] Penting." {
		t.Fatalf("res %q err %v", res.Text, err)
	}
}

// node membuat node uji; anak-anaknya menentukan ukuran folder.
func tnode(name string, size int64, kids ...*node) *node {
	n := &node{name: name, path: name, size: size, dir: len(kids) > 0 || size == 0}
	for _, k := range kids {
		k.path = name + `\` + k.name
		n.children = append(n.children, k)
		n.size += k.size
	}
	if len(kids) > 0 {
		n.dir = true
	}
	return n
}

func TestPickCandidates(t *testing.T) {
	const mb = 1 << 20
	ext := tnode("extensions", 0,
		tnode("dart-code.dart-code-3.144.0", 0, tnode("a.js", 15*mb)),
		tnode("dart-code.dart-code-3.146.0", 0, tnode("a.js", 15*mb)),
		tnode("vue.volar-3.3.13", 0, tnode("a.js", 10*mb)),
		tnode("openai.chatgpt-26.1-win32-x64", 0, tnode("bin", 0, tnode("x.exe", 1500*mb))),
	)
	nm := tnode("node_modules", 0, tnode("x.js", 100*mb))
	nm.collapsed = true
	root := tnode("root", 0, ext, nm, tnode("kecil.txt", 1*mb))
	lim := Limits{MaxLines: 1000, MinSize: 1 * mb}
	expand(root, lim)
	s := render(root, lim)

	var names []string
	facts := make(map[string]string)
	for _, c := range s.Candidates {
		it := s.Items[c.ID]
		names = append(names, it.Path[strings.LastIndex(it.Path, `\`)+1:])
		facts[names[len(names)-1]] = strings.Join(c.Facts, " · ")
	}
	// "extensions" adalah wadah, jadi yang dinilai isinya; folder ekstensi yang
	// besar tidak dipecah lagi (bin\ tidak muncul); node_modules tetap satu unit;
	// file di bawah 0,5% dari total dilewati.
	want := []string{"openai.chatgpt-26.1-win32-x64", "node_modules", "dart-code.dart-code-3.144.0", "dart-code.dart-code-3.146.0", "vue.volar-3.3.13"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("kandidat = %v, want %v", names, want)
	}
	for name, f := range map[string]string{
		"dart-code.dart-code-3.144.0": "ada versi lebih baru: dart-code.dart-code-3.146.0",
		"dart-code.dart-code-3.146.0": "versi terbaru dari 2 versi",
		"vue.volar-3.3.13":            "satu-satunya versi",
		"node_modules":                "isinya tidak dirinci",
	} {
		if !strings.Contains(facts[name], f) {
			t.Errorf("fakta %s = %q, want memuat %q", name, facts[name], f)
		}
	}
	// Daftar wajib ikut terkirim ke AI.
	if p := UserPrompt(s); !strings.Contains(p, "ITEM YANG HARUS DINILAI (5 item") || !strings.Contains(p, `extensions\vue.volar-3.3.13\`) {
		t.Errorf("prompt tidak memuat daftar wajib:\n%s", p)
	}
}
func TestTidyAnswer(t *testing.T) {
	// Seluruh jawaban dalam satu paragraf dipulihkan menjadi kelompok dan butir.
	flat := "Folder ekstensi. Aman dihapus: • [[1]] [[2]] Versi lama. • [[3]] Cache. Perlu dicek dulu: • [[4]] Besar."
	want := "Folder ekstensi.\n\nAman dihapus:\n• [[1]] [[2]] Versi lama.\n• [[3]] Cache.\n\nPerlu dicek dulu:\n• [[4]] Besar."
	if got := tidyAnswer(flat); got != want {
		t.Errorf("flat:\n got %q\nwant %q", got, want)
	}
	// Jawaban yang sudah rapi tidak berubah, dan hasilnya stabil jika diulang.
	if got := tidyAnswer(want); got != want {
		t.Errorf("rapi berubah:\n got %q\nwant %q", got, want)
	}
	// Jawaban tanpa judul atau butir tidak disentuh.
	if got := tidyAnswer("Hanya satu kalimat."); got != "Hanya satu kalimat." {
		t.Errorf("polos berubah: %q", got)
	}
	// Judul di awal jawaban tidak meninggalkan baris kosong di depan.
	if got := tidyAnswer("Aman dihapus: • [[1]] x"); got != "Aman dihapus:\n• [[1]] x" {
		t.Errorf("judul di awal: %q", got)
	}
}
func TestPickCandidatesChain(t *testing.T) {
	const mb = 1 << 20
	lim := Limits{MaxLines: 1000, MinSize: 1 * mb}
	names := func(root *node) string {
		expand(root, lim)
		s := render(root, lim)
		var out []string
		for _, c := range s.Candidates {
			p := s.Items[c.ID].Path
			out = append(out, p[strings.LastIndex(p, `\`)+1:])
		}
		return strings.Join(out, ",")
	}

	// C:\Users: profil satu-satunya dipecah; AppData dan Local adalah wadah;
	// go hanya separuh profil, jadi tetap satu unit.
	users := tnode("Users", 0, tnode("me", 0,
		tnode("AppData", 0, tnode("Local", 0,
			tnode("Chrome", 0, tnode("data", 500*mb)),
			tnode("Temp", 0, tnode("t", 300*mb)))),
		tnode("go", 0, tnode("pkg", 0, tnode("mod", 0, tnode("cache", 0, tnode("c", 400*mb)), tnode("golang.org", 0, tnode("g", 600*mb))))),
		tnode("Downloads", 0, tnode("d", 200*mb)),
	))
	if got := names(users); got != "go,Chrome,Temp,Downloads" {
		t.Errorf("Users: %s", got)
	}

	// Menganalisis go: pkg lalu mod mengisi hampir seluruhnya, jadi dipecah
	// sampai isinya yang bermakna.
	gopath := tnode("go", 0, tnode("pkg", 0, tnode("mod", 0, tnode("cache", 0, tnode("c", 400*mb)), tnode("golang.org", 0, tnode("g", 600*mb)))))
	if got := names(gopath); got != "golang.org,cache" {
		t.Errorf("go: %s", got)
	}
}