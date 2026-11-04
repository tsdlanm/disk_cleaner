package ui

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// place adalah folder bermakna tempat file baru dikelompokkan, misalnya
// "AppData\Local\Google\Chrome" atau "Program Files (x86)\Microsoft\Copilot",
// bukan folder terdalam seperti "go-build\64".
type place struct {
	drive  int // indeks ke m.recent.Drives
	dir    string
	files  []rfile // dari yang terbesar
	size   int64
	newest time.Time
	latest string // path file terbaru
}

// vendorDirs adalah folder penerbit yang isinya baru bermakna satu tingkat
// lebih dalam, misalnya AppData\Local\Google\Chrome.
var vendorDirs = map[string]bool{
	"google": true, "microsoft": true, "packages": true, "programs": true, "jetbrains": true,
	"mozilla": true, "bravesoftware": true, "apple": true, "adobe": true, "nvidia": true,
	"nvidia corporation": true, "intel": true, "amd": true, "temp": true,
}

// homeDir adalah folder profil pengguna, atau "" jika tidak diketahui.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Clean(h)
}

// under melaporkan apakah path sama dengan dir atau berada di dalamnya.
func under(path, dir string) bool {
	if dir == "" || len(path) < len(dir) || !strings.EqualFold(path[:len(dir)], dir) {
		return false
	}
	return len(path) == len(dir) || path[len(dir)] == '\\' || strings.HasSuffix(dir, `\`)
}

// longHome mengganti awalan nama pendek 8.3 folder profil (misalnya
// C:\Users\TA099~1.MUS dari variabel TEMP) dengan nama panjangnya.
func (m *model) longHome(path string) string {
	home := homeDir()
	if m.shortHome == "" {
		m.shortHome = "-"
		if p, err := windows.UTF16PtrFromString(home); err == nil && home != "" {
			buf := make([]uint16, windows.MAX_PATH)
			if n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf))); err == nil && n > 0 && int(n) < len(buf) {
				m.shortHome = windows.UTF16ToString(buf[:n])
			}
		}
	}
	if s := m.shortHome; s != "-" && !strings.EqualFold(s, home) && under(path, s) {
		return home + path[len(s):]
	}
	return path
}

// placeOf menentukan tempat bermakna untuk file baru path.
func (m *model) placeOf(path string) string {
	parent := filepath.Dir(m.longHome(path))
	split := func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, `\`) {
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	join := func(base string, parts []string, n int) string {
		return tidy(filepath.Join(append([]string{base}, parts[:min(n, len(parts))]...)...), base)
	}

	if home := homeDir(); under(parent, home) {
		r := split(parent[len(home):])
		switch {
		case len(r) == 0:
			return home
		case strings.EqualFold(r[0], "AppData"):
			n := 3 // AppData\Local\<aplikasi>
			if len(r) >= 3 && vendorDirs[strings.ToLower(r[2])] {
				n = 4
			}
			return join(home, r, n)
		case strings.HasPrefix(r[0], "."):
			return join(home, r, 1) // .vscode, .gradle, …
		}
		return join(home, r, 2) // Downloads\film, Documents\proyek
	}

	root := filepath.VolumeName(parent) + `\`
	r := split(parent[len(root)-1:])
	if len(r) == 0 {
		return root
	}
	n := 2
	switch strings.ToLower(r[0]) {
	case "windows", "program files", "program files (x86)", "programdata", "users":
		if len(r) >= 2 && vendorDirs[strings.ToLower(r[1])] {
			n = 3 // Program Files (x86)\Microsoft\Copilot
		}
	case "$recycle.bin":
		n = 1
	}
	return join(root, r, n)
}

// tidy naik ke folder induk selama nama folder terakhir acak (hash/GUID) atau
// folder penyimpan cache, tapi tidak melewati base.
func tidy(dir, base string) string {
	for noisyName(filepath.Base(dir)) && len(dir) > len(base) && filepath.Dir(dir) != dir {
		dir = filepath.Dir(dir)
	}
	return dir
}

// cacheLeaf adalah nama folder penyimpan cache yang tidak bermakna sendiri.
var cacheLeaf = map[string]bool{
	"cache": true, "cache_data": true, "code cache": true, "gpucache": true, "cache2": true,
	"entries": true, "js": true, "wasm": true, "index-dir": true, "doomed": true,
}

// noisyName melaporkan apakah nama folder tidak bermakna bagi pengguna: nama
// hash/heksadesimal acak (go-build\1b, GUID) atau folder penyimpan cache.
func noisyName(name string) bool {
	n := strings.Trim(strings.ToLower(name), "{}")
	if cacheLeaf[n] {
		return true
	}
	digit := false
	for _, r := range n {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'f', r == '-':
		default:
			return false
		}
	}
	// Dua karakter (go-build\1b) atau hash/GUID panjang; nama pendek seperti
	// tahun "2024" atau "d00" tetap dianggap bermakna.
	return len(n) == 2 || (digit && len(n) >= 12) || len(n) >= 16
}

// places mengelompokkan file baru per tempat, per drive (urutan
// m.recent.Drives), lalu dari yang terbaru. File di dalam tempat diurutkan
// dari yang terbesar.
func (m *model) places() []place {
	if m.rplaces != nil {
		return m.rplaces
	}
	out := []place{}
	byDir := make(map[string]int)
	for _, f := range m.recentFiles() {
		dir := m.placeOf(f.Path)
		key := strings.ToLower(dir)
		i, ok := byDir[key]
		if !ok {
			i = len(out)
			byDir[key] = i
			out = append(out, place{drive: f.drive, dir: dir})
		}
		p := &out[i]
		p.files = append(p.files, f)
		p.size += f.Size
		if f.Created.After(p.newest) {
			p.newest, p.latest = f.Created, f.Path
		}
	}
	for i := range out {
		slices.SortStableFunc(out[i].files, func(a, b rfile) int { return cmp.Compare(b.Size, a.Size) })
	}
	slices.SortStableFunc(out, func(a, b place) int {
		if a.drive != b.drive {
			return cmp.Compare(a.drive, b.drive)
		}
		if m.rbysize && a.size != b.size {
			return cmp.Compare(b.size, a.size)
		}
		return b.newest.Compare(a.newest)
	})
	m.rplaces = out
	return out
}

// tildePath menyingkat folder profil pengguna menjadi "~".
func tildePath(path string) string {
	if home := homeDir(); under(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
