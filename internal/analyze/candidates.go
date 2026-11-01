package analyze

import (
	"cmp"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tsdlanm/cli_app/internal/format"
)

// Candidate adalah satu item yang wajib dinilai AI, beserta fakta yang
// dihitung aplikasi (bukan tebakan AI).
type Candidate struct {
	ID    int
	Facts []string
}

// maxCandidates membatasi jumlah item yang dinilai dalam satu analisis.
const maxCandidates = 40

// containerDirs adalah folder wadah atau penerbit yang isinya baru bermakna
// satu tingkat lebih dalam, misalnya AppData\Local\<aplikasi>,
// AppData\Local\Google\Chrome, atau .vscode\extensions\<ekstensi>.
var containerDirs = map[string]bool{
	"appdata": true, "local": true, "roaming": true, "locallow": true, "extensions": true,
	"users": true, "programdata": true, "program files": true, "program files (x86)": true,
	"programs": true, "packages": true, "google": true, "microsoft": true, "bravesoftware": true,
	"mozilla": true, "jetbrains": true, "adobe": true, "nvidia corporation": true,
}

// pickCandidates memilih item yang dinilai AI: dimulai dari isi folder yang
// dianalisis, lalu folder wadah (AppData, extensions, …) dan folder yang
// hampir seluruhnya mengisi induknya di sepanjang rantai dari folder yang
// dianalisis (C:\Users → T. Mustaqim, go → pkg → mod) diganti dengan isinya.
// Hasilnya unit yang bermakna (satu aplikasi, satu ekstensi, satu cache);
// isi folder wadah tidak dipecah lagi walaupun besar. Item yang sangat kecil
// dilewati.
func pickCandidates(root *node, lim Limits, ids map[*node]int, now time.Time) []Candidate {
	// chain: induknya adalah folder yang dianalisis atau folder yang dipecah
	// karena hampir seluruhnya mengisi induknya.
	type entry struct {
		n, parent *node
		chain     bool
	}
	kids := func(n *node, chain bool) []entry {
		list, _, _ := shown(n, lim)
		var out []entry
		for _, c := range list {
			if _, ok := ids[c]; ok {
				out = append(out, entry{c, n, chain})
			}
		}
		return out
	}

	list := kids(root, true)
	for changed := true; changed; {
		changed = false
		for i, e := range list {
			if !e.n.dir || !e.n.expanded || e.n.collapsed {
				continue
			}
			container := containerDirs[strings.ToLower(e.n.name)]
			dominant := e.chain && e.n.size*10 >= e.parent.size*9
			if !container && !dominant {
				continue
			}
			if sub := kids(e.n, !container); len(sub) > 0 {
				list = slices.Replace(list, i, i+1, sub...)
				changed = true
				break
			}
		}
	}

	minSize := max(lim.MinSize, root.size/200)
	list = slices.DeleteFunc(list, func(e entry) bool { return e.n.size < minSize })
	slices.SortStableFunc(list, func(a, b entry) int { return cmp.Compare(b.n.size, a.n.size) })
	list = list[:min(len(list), maxCandidates)]

	out := make([]Candidate, 0, len(list))
	for _, e := range list {
		out = append(out, Candidate{ID: ids[e.n], Facts: facts(e.n, e.parent, now)})
	}
	return out
}

// facts menyusun fakta tentang n yang membantu AI menilai: jenis, umur, dan
// versi dibandingkan saudara-saudaranya.
func facts(n, parent *node, now time.Time) []string {
	var out []string
	if n.dir {
		out = append(out, "folder, "+format.Int(n.files)+" file")
	} else {
		out = append(out, "file")
	}
	if !n.mod.IsZero() {
		days := int(now.Sub(n.mod).Hours() / 24)
		out = append(out, fmt.Sprintf("terakhir diubah %s (%s)", n.mod.Format("2006-01-02"), ago(days)))
	}
	switch {
	case n.denied:
		out = append(out, "akses ditolak")
	case n.collapsed:
		out = append(out, "isinya tidak dirinci")
	}
	if f := versionFact(n, parent); f != "" {
		out = append(out, f)
	}
	return out
}

func ago(days int) string {
	switch {
	case days <= 0:
		return "hari ini"
	case days < 60:
		return fmt.Sprintf("%d hari lalu", days)
	case days < 730:
		return fmt.Sprintf("%d bulan lalu", days/30)
	}
	return fmt.Sprintf("%d tahun lalu", days/365)
}

// versionRe memecah nama seperti "dart-code.dart-code-3.144.0" atau
// "anthropic.claude-code-2.1.312-win32-x64" menjadi awalan, versi, dan akhiran.
var versionRe = regexp.MustCompile(`^(.*?)(\d+(?:\.\d+)+)(.*)$`)

// versionFact membandingkan versi n dengan saudara yang namanya sama selain
// nomor versinya: "ada versi lebih baru: …", "versi terbaru dari N versi",
// atau "satu-satunya versi". Kosong jika nama n tidak memuat versi.
func versionFact(n, parent *node) string {
	key, ver, ok := splitVersion(n.name)
	if !ok || parent == nil {
		return ""
	}
	var newer string
	var newerVer []int
	count := 1
	for _, s := range parent.children {
		if s == n || s.dir != n.dir {
			continue
		}
		k, v, ok := splitVersion(s.name)
		if !ok || k != key {
			continue
		}
		count++
		if slices.Compare(v, ver) > 0 && (newer == "" || slices.Compare(v, newerVer) > 0) {
			newer, newerVer = s.name, v
		}
	}
	switch {
	case newer != "":
		return "ada versi lebih baru: " + newer
	case count > 1:
		return fmt.Sprintf("versi terbaru dari %d versi", count)
	}
	return "satu-satunya versi"
}

func splitVersion(name string) (key string, ver []int, ok bool) {
	m := versionRe.FindStringSubmatch(name)
	if m == nil {
		return "", nil, false
	}
	for _, p := range strings.Split(m[2], ".") {
		v, err := strconv.Atoi(p)
		if err != nil {
			return "", nil, false
		}
		ver = append(ver, v)
	}
	return strings.ToLower(m[1]) + "|" + strings.ToLower(m[3]), ver, true
}

// CandidateText adalah daftar item yang wajib dinilai, satu baris per item:
// "[id] path · ukuran · fakta…". Path ditulis relatif terhadap folder yang
// dianalisis.
func (s *Snapshot) CandidateText() string {
	var b strings.Builder
	for _, c := range s.Candidates {
		it := s.Items[c.ID]
		name := it.Path
		if r, err := filepath.Rel(s.Root, it.Path); err == nil && r != "." {
			name = r
		}
		if it.IsDir {
			name += `\`
		}
		fmt.Fprintf(&b, "[%d] %s · %s", c.ID, name, format.Bytes(it.Size))
		for _, f := range c.Facts {
			b.WriteString(" · " + f)
		}
		b.WriteString("\n")
	}
	return b.String()
}
