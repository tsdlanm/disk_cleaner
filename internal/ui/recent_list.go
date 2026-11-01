package ui

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/recent"
)

// Lebar kolom daftar file.
const (
	recentNameW  = 44 // nama folder/file terluar
	recentCountW = 10 // "1.234 file"
	recentSizeW  = 10 // "1023.9 MB"
	recentTimeW  = 13 // "23 menit lalu"
)

// rfile adalah satu file baru beserta drive-nya (indeks ke m.recent.Drives).
type rfile struct {
	recent.File
	drive int
}

// recentFiles mengembalikan file baru dalam recent.Window, per drive (urutan
// m.recent.Drives), di dalam drive dari yang terbaru. Cache, aplikasi, dan
// file pengguna diperlakukan sama, karena semuanya memakai ruang.
func (m *model) recentFiles() []rfile {
	if m.rfiles != nil {
		return m.rfiles
	}
	out := []rfile{}
	if m.recent != nil {
		since := time.Now().Add(-recent.Window)
		for i, d := range m.recent.Drives {
			for _, f := range d.Files {
				if f.Created.Before(since) {
					break // urut dari yang terbaru
				}
				out = append(out, rfile{f, i})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b rfile) int {
		if a.drive != b.drive {
			return cmp.Compare(a.drive, b.drive)
		}
		return b.Created.Compare(a.Created)
	})
	m.rfiles = out
	return out
}

// recentPage adalah jumlah folder yang tampil per drive, dan tambahannya
// setiap kali "tampilkan lainnya" dipilih.
const recentPage = 10

// Nilai place khusus di rline.
const (
	placeTitle = -1 // judul drive
	placeMore  = -2 // "tampilkan lainnya"
)

// rline adalah satu baris bagian "File baru": judul drive, satu tempat
// (indeks ke places), atau tombol "tampilkan lainnya" untuk satu drive.
type rline struct {
	drive int
	place int
}

func (m *model) shownFor(drive int) int {
	if n, ok := m.rshown[drive]; ok {
		return n
	}
	return recentPage
}

// recentLines menyusun baris bagian "File baru": per drive, judulnya, lalu
// paling banyak shownFor tempat, dan "tampilkan lainnya" jika masih ada.
func (m *model) recentLines() []rline {
	var out []rline
	ps := m.places()
	for i := 0; i < len(ps); {
		d := ps[i].drive
		j := i
		for j < len(ps) && ps[j].drive == d {
			j++
		}
		out = append(out, rline{drive: d, place: placeTitle})
		if m.rclosed[d] {
			i = j // ditutup: tinggal judul drive
			continue
		}
		n := min(j-i, m.shownFor(d))
		for k := range n {
			out = append(out, rline{drive: d, place: i + k})
		}
		if n < j-i {
			out = append(out, rline{drive: d, place: placeMore})
		}
		i = j
	}
	return out
}

// recentSelectable mengembalikan indeks baris yang bisa dipilih kursor.
// Semuanya bisa dipilih, termasuk judul drive (enter membuka/menutupnya).
func recentSelectable(lines []rline) []int {
	out := make([]int, len(lines))
	for i := range lines {
		out[i] = i
	}
	return out
}

// collapseStep meringkas drive satu tahap, kebalikan dari "tampilkan
// lainnya": selama yang tampil lebih dari satu halaman, turunkan satu
// halaman (misalnya 30 → 20 → 10); setelah itu tutup seluruh isinya sampai
// tinggal judul drive.
func (m *model) collapseStep(drive int) {
	visible := m.visibleCount(drive)
	if visible <= recentPage {
		m.toggleDrive(drive, true)
		return
	}
	if m.rshown == nil {
		m.rshown = make(map[int]int)
	}
	m.rshown[drive] = (visible - 1) / recentPage * recentPage
}

// toggleDrive membuka atau menutup isi drive di "File baru"; kursor pindah
// ke judul drive itu.
func (m *model) toggleDrive(drive int, closed bool) {
	if m.rclosed == nil {
		m.rclosed = make(map[int]bool)
	}
	m.rclosed[drive] = closed
	for k, l := range m.recentLines() {
		if l.drive == drive && l.place == placeTitle {
			m.rcursor = k
			return
		}
	}
}

// recentCurrent mengembalikan baris di kursor.
func (m *model) recentCurrent() (rline, bool) {
	lines := m.recentLines()
	sel := recentSelectable(lines)
	if len(sel) == 0 {
		return rline{}, false
	}
	m.rcursor = min(max(m.rcursor, 0), len(sel)-1)
	return lines[sel[m.rcursor]], true
}

// selectedRecent mengembalikan folder tempat di kursor, atau "" jika kursor
// tidak di bagian "File baru" atau tidak di baris tempat.
func (m *model) selectedRecent() string {
	if !m.focusRecent {
		return ""
	}
	if l, ok := m.recentCurrent(); ok && l.place >= 0 {
		return m.places()[l.place].dir
	}
	return ""
}

// restoreRecent memindahkan kursor kembali ke tempat dir setelah daftar
// berubah (misalnya tempat lain mendapat file baru dan naik ke atas).
func (m *model) restoreRecent(dir string) {
	ps := m.places()
	i := slices.IndexFunc(ps, func(p place) bool { return p.dir == dir })
	if i < 0 {
		return
	}
	// Tempat yang naik dari luar halaman tetap terlihat.
	first := i
	for first > 0 && ps[first-1].drive == ps[i].drive {
		first--
	}
	if m.rshown == nil {
		m.rshown = make(map[int]int)
	}
	m.rshown[ps[i].drive] = max(m.shownFor(ps[i].drive), i-first+1)
	lines := m.recentLines()
	for k, li := range recentSelectable(lines) {
		if lines[li].place == i {
			m.rcursor = k
			return
		}
	}
}

// handleRecentKey menangani tombol saat kursor di bagian "File baru".
// handled false berarti tombol diteruskan ke penanganan biasa.
func (m *model) handleRecentKey(key string) (cmd tea.Cmd, handled bool) {
	l, ok := m.recentCurrent()
	if !ok {
		m.focusRecent = false
		return nil, false
	}
	n := len(recentSelectable(m.recentLines()))
	var p place
	if l.place >= 0 {
		p = m.places()[l.place]
	}
	switch key {
	case "esc":
		m.focusRecent = false
	case "up", "k":
		// Dari baris pertama kembali ke drive terakhir.
		if m.rcursor == 0 {
			m.focusRecent = false
			m.cursor = len(m.drives) - 1
		} else {
			m.rcursor--
		}
	case "down", "j":
		m.rcursor = min(m.rcursor+1, n-1)
	case "pgup":
		m.rcursor = max(m.rcursor-m.listHeight(), 0)
	case "pgdown":
		m.rcursor = min(m.rcursor+m.listHeight(), n-1)
	case "home", "g":
		m.rcursor = 0
	case "end", "G":
		m.rcursor = n - 1
	case "left", "h":
		// Tutup drive-nya: tinggal judul drive.
		m.toggleDrive(l.drive, true)
	case "enter", "right", "l":
		if l.place == placeTitle {
			closed := m.rclosed[l.drive]
			switch {
			case key == "enter" && closed:
				m.toggleDrive(l.drive, false)
			case key == "enter":
				m.collapseStep(l.drive) // tutup hasil "tampilkan lainnya" dulu, lalu semuanya
			case closed:
				m.toggleDrive(l.drive, false) // → hanya membuka
			}
			return nil, true
		}
		if l.place == placeMore {
			// Kursor tetap di posisi yang sama: folder pertama yang baru tampil.
			if m.rshown == nil {
				m.rshown = make(map[int]int)
			}
			m.rshown[l.drive] = m.shownFor(l.drive) + recentPage
			return nil, true
		}
		// File sementara (misalnya hasil go build) bisa sudah hilang sebelum
		// pemantauan sempat mencatatnya: bersihkan dari daftar, jangan error.
		if !m.dirExists(p.dir) {
			paths := []string{p.dir}
			for _, f := range p.files {
				paths = append(paths, f.Path)
			}
			m.applyRecent(recent.Check(paths, m.recentOptions(), time.Now()))
			m.status = filepath.Base(p.dir) + " sudah tidak ada (file sementara atau sudah dihapus); daftar diperbarui"
			return nil, true
		}
		// Masuk ke folder itu, kursor di subfolder/file berisi file terbarunya.
		if rel, err := filepath.Rel(p.dir, p.latest); err == nil {
			m.remember[p.dir] = strings.Split(rel, `\`)[0]
		}
		return loadDir(p.dir), true
	case "o":
		if l.place >= 0 {
			m.status = runExplorer(`explorer.exe "` + p.dir + `"`)
		}
	case "a":
		if l.place >= 0 {
			return m.analyzePath(p.dir), true
		}
	case "r":
		return m.startRecent(), true
	default:
		return nil, false
	}
	return nil, true
}

// recentSummary adalah baris ringkasan halaman "File baru".
func (m *model) recentSummary() string {
	switch {
	case m.recentRunning && m.recent == nil:
		return dimStyle.Render(fmt.Sprintf("memindai semua drive… %s file", format.Int(m.recentProg.Files.Load())))
	case m.recentErr != nil && m.recent == nil:
		return warnStyle.Render("gagal memindai: " + m.recentErr.Error())
	case m.recent == nil:
		return dimStyle.Render("belum dipindai · r pindai")
	}
	var size int64
	files := m.recentFiles()
	for _, f := range files {
		size += f.Size
	}
	s := dimStyle.Render(fmt.Sprintf("%s file · %s · dari yang terbaru",
		format.Int(int64(len(files))), format.Bytes(size)))
	// Data yang tampil bisa berasal dari hasil tersimpan; tunjukkan umurnya,
	// dan apakah perubahan sejak itu terus diikuti.
	switch {
	case m.recentRunning:
		s += dimStyle.Render(fmt.Sprintf(" · memindai ulang… %s file", format.Int(m.recentProg.Files.Load())))
	default:
		s += dimStyle.Render(" · dipindai " + relTime(m.recentAt))
	}
	switch {
	case m.liveLost:
		s += warnStyle.Render(" · sebagian perubahan terlewat, r pindai ulang")
	case m.watcher != nil:
		s += newStyle.Render(" · ● langsung")
	}
	return s
}

// viewRecent menulis bagian "File baru" di bawah daftar drive, mengisi
// paling banyak h baris. Mengembalikan jumlah baris yang ditulis.
func (m *model) viewRecent(b *strings.Builder, h int) int {
	m.line(b, titleStyle.Render("File baru")+dimStyle.Render(" · 24 jam terakhir · ")+m.recentSummary())
	if len(m.recentFiles()) == 0 {
		if m.recent != nil && h > 1 {
			m.line(b, dimStyle.Render(" Tidak ada file baru dalam 24 jam terakhir."))
			return 2
		}
		return 1
	}
	lines := m.recentLines()
	ps := m.places()
	listH := h - 2
	cur := -1
	if l, ok := m.recentCurrent(); m.focusRecent && ok {
		cur = slices.Index(lines, l)
		switch {
		case l.place >= 0:
			m.line(b, " "+titleStyle.Render(truncateLeft(m.placePath(ps[l.place]), max(m.screenWidth()-2, 10))))
		case l.place == placeMore:
			m.line(b, dimStyle.Render(fmt.Sprintf(" enter: tampilkan %d folder lagi", min(recentPage, m.moreLeft(l.drive)))))
		case m.rclosed[l.drive]:
			m.line(b, dimStyle.Render(" enter: buka daftar folder "+m.recent.Drives[l.drive].Root))
		case m.visibleCount(l.drive) > recentPage:
			m.line(b, dimStyle.Render(fmt.Sprintf(" enter: kurangi jadi %d folder", (m.visibleCount(l.drive)-1)/recentPage*recentPage)))
		default:
			m.line(b, dimStyle.Render(" enter: tutup daftar folder "+m.recent.Drives[l.drive].Root))
		}
	} else {
		m.line(b, dimStyle.Render(" ↓ dari drive terakhir untuk memilih; path lengkapnya tampil di sini"))
	}
	if cur >= 0 {
		// Saat menggulir ke atas, judul drive di atas baris pertamanya ikut terlihat.
		top := cur
		if top > 0 && lines[top-1].place == placeTitle {
			top--
		}
		if top < m.roffset {
			m.roffset = top
		}
		if cur >= m.roffset+listH {
			m.roffset = cur - listH + 1
		}
	}
	m.roffset = min(max(m.roffset, 0), max(len(lines)-listH, 0))

	// Kolom nama dibatasi agar jumlah, ukuran, dan waktu tidak jauh dari namanya,
	// dan disisakan satu kolom agar baris tidak terpotong di tepi layar.
	nameW := min(max(m.screenWidth()-4-2-recentCountW-2-recentSizeW-2-recentTimeW-1, 10), recentNameW)
	used := 2
	for i := m.roffset; i < min(m.roffset+listH, len(lines)); i++ {
		l := lines[i]
		switch {
		case l.place == placeTitle:
			m.line(b, m.recentDriveTitle(l.drive, i == cur))
		case l.place == placeMore:
			text := fmt.Sprintf("▸ tampilkan lainnya (%s folder lagi)", format.Int(int64(m.moreLeft(l.drive))))
			if i == cur {
				text = selStyle.Render(text)
			} else {
				text = dimStyle.Render(text)
			}
			m.line(b, "    "+text)
		default:
			m.line(b, m.renderPlace(ps[l.place], i == cur, nameW))
		}
		used++
	}
	return used
}

// driveTotal adalah jumlah folder berisi file baru di drive.
func (m *model) driveTotal(drive int) int {
	n := 0
	for _, p := range m.places() {
		if p.drive == drive {
			n++
		}
	}
	return n
}

// visibleCount adalah jumlah folder drive yang benar-benar tampil.
func (m *model) visibleCount(drive int) int {
	return min(m.shownFor(drive), m.driveTotal(drive))
}

// moreLeft adalah jumlah folder di drive yang belum tampil.
func (m *model) moreLeft(drive int) int {
	return m.driveTotal(drive) - m.visibleCount(drive)
}

// soloFile melaporkan apakah p ditampilkan sebagai satu file bernama. Jika
// satu-satunya file bernama hash acak (objek git, blob cache), nama itu tidak
// bermakna, jadi yang ditampilkan nama foldernya.
func soloFile(p place) bool {
	return len(p.files) == 1 && !noisyName(filepath.Base(p.files[0].Path))
}

// placePath adalah path lengkap tempat p: folder tempatnya, atau file itu
// sendiri jika hanya berisi satu file.
func (m *model) placePath(p place) string {
	if soloFile(p) {
		return m.longHome(p.files[0].Path)
	}
	return strings.TrimSuffix(p.dir, `\`) + `\`
}

// renderPlace menulis satu tempat: nama folder terluarnya saja (misalnya
// "go-build\"), atau nama filenya jika hanya berisi satu file, lalu jumlah
// file baru, ukuran, dan kapan file terbarunya dibuat. Path lengkapnya
// ditampilkan di atas bagian ini saat kursor di baris itu.
func (m *model) renderPlace(p place, selected bool, nameW int) string {
	var name, count string
	isFile := soloFile(p)
	if isFile {
		name = filepath.Base(p.files[0].Path)
	} else {
		count = format.Int(int64(len(p.files))) + " file"
		name = filepath.Base(p.dir) + `\`
		if filepath.Dir(p.dir) == p.dir {
			name = p.dir // root drive
		}
	}
	name = runewidth.FillRight(truncateMiddle(name, nameW), nameW)
	switch {
	case selected:
		name = selStyle.Render(name)
	case !isFile:
		name = dirStyle.Render(name)
	}
	return "    " + name + "  " + dimStyle.Render(fmt.Sprintf("%*s", recentCountW, count)) + "  " +
		fmt.Sprintf("%*s", recentSizeW, format.Bytes(p.size)) + "  " + dimStyle.Render(relTime(p.newest))
}

// clockTime menampilkan jam pasti beserta waktu relatifnya, misalnya
// "14:05 · 2 jam lalu", agar mudah dicocokkan dengan kegiatan pengguna.
func clockTime(t time.Time) string {
	return t.Format("15:04") + " · " + relTime(t)
}

// recentDriveTitle adalah judul bagian satu drive: jumlah file baru, jumlah
// folder, dan ukurannya, dalam rentang yang sama dengan daftar di bawahnya
// (24 jam). Pertambahan ukuran drive sejak pemindaian lama tidak ditampilkan
// di sini, karena rentangnya berbeda dan membingungkan.
func (m *model) recentDriveTitle(i int, selected bool) string {
	d := m.recent.Drives[i]
	n, size, places := 0, int64(0), 0
	for _, f := range m.recentFiles() {
		if f.drive == i {
			n++
			size += f.Size
		}
	}
	for _, p := range m.places() {
		if p.drive == i {
			places++
		}
	}
	arrow := "▾"
	if m.rclosed[i] {
		arrow = "▸"
	}
	text := " " + arrow + " " + d.Root
	if di := m.driveOf(d.Root); di >= 0 && m.drives[di].Label != "" {
		text += " " + m.drives[di].Label
	}
	info := fmt.Sprintf(" · %s file baru · %d folder · %s", format.Int(int64(n)), places, format.Bytes(size))
	if selected {
		return selStyle.Render(text) + dimStyle.Render(info)
	}
	return titleStyle.Render(dirStyle.Render(text)) + dimStyle.Render(info)
}

// pathCell menampilkan path lengkap selebar w: foldernya redup, nama file
// normal. Path yang terlalu panjang dipotong di awal agar nama file terlihat.
func pathCell(path string, w int, selected bool) string {
	s := runewidth.FillRight(truncateLeft(path, w), w)
	if selected {
		return selStyle.Render(s)
	}
	i := strings.LastIndex(s, `\`)
	if i < 0 {
		return s
	}
	return dimStyle.Render(s[:i+1]) + s[i+1:]
}

// truncateMiddle memotong bagian tengah teks panjang, misalnya nama hash
// "88809242bdf…c43904-d", agar awal dan akhirnya tetap terlihat.
func truncateMiddle(s string, w int) string {
	if runewidth.StringWidth(s) <= w {
		return s
	}
	r := []rune(s)
	head := (w - 1) / 2
	tail := w - 1 - head
	for runewidth.StringWidth(string(r[:head])) > (w-1)/2 {
		head--
	}
	for runewidth.StringWidth(string(r[len(r)-tail:])) > w-1-runewidth.StringWidth(string(r[:head])) {
		tail--
	}
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
