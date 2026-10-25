package ui

import (
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

// recentPage adalah jumlah folder yang ditampilkan per bagian, dan jumlah
// tambahan setiap kali "tampilkan lagi" dipilih.
const recentPage = 5

// folderGroup adalah ringkasan file baru di satu folder (beserta subfolder
// cache/acak yang digabung ke dalamnya, lihat groupDir).
type folderGroup struct {
	dir    string
	kind   recent.Kind // asal file: pengguna, aplikasi, cache, sistem
	count  int
	size   int64
	newest time.Time
	latest string // path file terbaru
}

// driveGroups adalah folder berisi file baru di satu drive, dipisah antara
// file pengguna dan file cache/aplikasi/sistem.
type driveGroups struct {
	user, other []folderGroup
	otherFiles  int
	otherSize   int64
}

// cacheLeaf adalah nama folder penyimpan cache yang tidak bermakna sendiri;
// isinya digabung ke folder induknya (misalnya Chrome "...\Profile 1\Cache\Cache_Data").
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

// groupDir adalah folder tempat file di dir dikelompokkan: dir itu sendiri,
// atau folder induk dari komponen acak/cache pertama di path-nya, misalnya
// "...\transforms\37ce…\transformed" menjadi "...\transforms".
func groupDir(dir string) string {
	vol := filepath.VolumeName(dir)
	cur := vol + `\`
	for _, name := range strings.Split(strings.Trim(dir[len(vol):], `\`), `\`) {
		if name == "" {
			continue
		}
		if noisyName(name) {
			return cur
		}
		cur = filepath.Join(cur, name)
	}
	return dir
}

// groups mengembalikan folder berisi file baru di drive i, urut dari folder
// yang file terbarunya paling baru.
func (m *model) groups(i int) driveGroups {
	if m.rgroups == nil {
		m.rgroups = make(map[int]driveGroups)
	}
	if g, ok := m.rgroups[i]; ok {
		return g
	}
	since := time.Now().AddDate(0, 0, -m.days())
	byDir := make(map[string]*folderGroup)
	var order []*folderGroup
	for _, f := range m.recent.Drives[i].Files {
		if f.Created.Before(since) {
			break // urut dari yang terbaru, jadi grup juga terbentuk urut terbaru
		}
		dir := groupDir(filepath.Dir(f.Path))
		g := byDir[dir]
		if g == nil {
			// Label dihitung ulang dari path, jadi aturan label terbaru juga berlaku
			// untuk data dari snapshot lama.
			g = &folderGroup{dir: dir, kind: recent.KindOf(f.Path), newest: f.Created, latest: f.Path}
			byDir[dir] = g
			order = append(order, g)
		}
		g.count++
		g.size += f.Size
	}
	var out driveGroups
	for _, g := range order {
		if g.kind == recent.KindUser {
			out.user = append(out.user, *g)
			continue
		}
		out.other = append(out.other, *g)
		out.otherFiles += g.count
		out.otherSize += g.size
	}
	m.rgroups[i] = out
	return out
}

// Jenis baris di bagian "File baru".
const (
	lineDrive  = iota // judul drive
	lineFolder        // satu folder berisi file baru (bisa dipilih)
	lineMore          // "tampilkan lagi" (bisa dipilih)
	lineOther         // ringkasan cache/aplikasi/sistem; enter membuka/menutup (bisa dipilih)
	lineEmpty         // "tidak ada file baru dari pengguna"
)

type recentLine struct {
	kind  int
	drive int  // indeks ke m.recent.Drives
	other bool // bagian cache/aplikasi/sistem
	group folderGroup
}

func (l recentLine) selectable() bool { return l.kind != lineDrive && l.kind != lineEmpty }

// pageKey adalah kunci jumlah tampil untuk satu bagian di satu drive.
func pageKey(root string, other bool) string {
	if other {
		return root + "|lain"
	}
	return root
}

func (m *model) shownFor(key string) int {
	if n, ok := m.recentShown[key]; ok {
		return n
	}
	return recentPage
}

// recentLines menyusun baris bagian "File baru": per drive, judul, folder
// pengguna, lalu ringkasan cache/aplikasi/sistem yang bisa dibuka.
func (m *model) recentLines() []recentLine {
	if m.recent == nil {
		return nil
	}
	var out []recentLine
	section := func(i int, other bool, groups []folderGroup) {
		key := pageKey(m.recent.Drives[i].Root, other)
		n := min(m.shownFor(key), len(groups))
		for _, g := range groups[:n] {
			out = append(out, recentLine{kind: lineFolder, drive: i, other: other, group: g})
		}
		if n < len(groups) {
			out = append(out, recentLine{kind: lineMore, drive: i, other: other})
		}
	}
	for i, d := range m.recent.Drives {
		out = append(out, recentLine{kind: lineDrive, drive: i})
		g := m.groups(i)
		if len(g.user) == 0 {
			out = append(out, recentLine{kind: lineEmpty, drive: i})
		}
		section(i, false, g.user)
		if len(g.other) > 0 {
			out = append(out, recentLine{kind: lineOther, drive: i})
			if m.rexpand[d.Root] {
				section(i, true, g.other)
			}
		}
	}
	return out
}

// selectableLines mengembalikan indeks baris yang bisa dipilih.
func selectableLines(lines []recentLine) []int {
	var out []int
	for i, l := range lines {
		if l.selectable() {
			out = append(out, i)
		}
	}
	return out
}

func (m *model) hasRecentFiles() bool {
	return len(selectableLines(m.recentLines())) > 0
}

// selectedRecent mengembalikan folder di kursor, atau "" jika kursor tidak
// di bagian "File baru" atau tidak berada di baris folder.
func (m *model) selectedRecent() string {
	if !m.focusRecent {
		return ""
	}
	lines := m.recentLines()
	sel := selectableLines(lines)
	if m.rcursor < 0 || m.rcursor >= len(sel) || lines[sel[m.rcursor]].kind != lineFolder {
		return ""
	}
	return lines[sel[m.rcursor]].group.dir
}

// restoreRecent memindahkan kursor kembali ke folder dir setelah daftar
// berubah (misalnya folder lain mendapat file baru dan naik ke atas).
func (m *model) restoreRecent(dir string) {
	if dir == "" {
		return
	}
	for i, d := range m.recent.Drives {
		g := m.groups(i)
		for _, part := range []struct {
			other  bool
			groups []folderGroup
		}{{false, g.user}, {true, g.other}} {
			if j := slices.IndexFunc(part.groups, func(g folderGroup) bool { return g.dir == dir }); j >= 0 {
				// Folder yang naik dari luar halaman tetap terlihat.
				key := pageKey(d.Root, part.other)
				m.recentShown[key] = max(m.shownFor(key), j+1)
			}
		}
	}
	lines := m.recentLines()
	for k, i := range selectableLines(lines) {
		if lines[i].kind == lineFolder && lines[i].group.dir == dir {
			m.rcursor = k
			return
		}
	}
}

// handleRecentKey menangani tombol saat kursor ada di bagian "File baru".
// handled false berarti tombol diteruskan ke penanganan biasa.
func (m *model) handleRecentKey(key string) (cmd tea.Cmd, handled bool) {
	lines := m.recentLines()
	sel := selectableLines(lines)
	n := len(sel)
	if n == 0 {
		m.focusRecent = false
		return nil, false
	}
	m.rcursor = min(max(m.rcursor, 0), n-1)
	l := lines[sel[m.rcursor]]
	root := m.recent.Drives[l.drive].Root
	switch key {
	case "tab", "esc":
		m.focusRecent = false
	case "up", "k":
		m.rcursor = (m.rcursor - 1 + n) % n
	case "down", "j":
		m.rcursor = (m.rcursor + 1) % n
	case "pgup":
		m.rcursor = max(m.rcursor-recentPage, 0)
	case "pgdown":
		m.rcursor = min(m.rcursor+recentPage, n-1)
	case "home", "g":
		m.rcursor = 0
	case "end", "G":
		m.rcursor = n - 1
	case "enter", "right", "l":
		switch l.kind {
		case lineMore:
			// Kursor tetap di posisi yang sama, yaitu folder pertama yang baru tampil.
			k := pageKey(root, l.other)
			m.recentShown[k] = m.shownFor(k) + recentPage
		case lineOther:
			if m.rexpand == nil {
				m.rexpand = make(map[string]bool)
			}
			m.rexpand[root] = !m.rexpand[root]
		case lineFolder:
			// Kursor langsung di file terbaru folder itu (atau subfolder yang memuatnya).
			if rel, err := filepath.Rel(l.group.dir, l.group.latest); err == nil {
				m.remember[l.group.dir] = strings.Split(rel, string(filepath.Separator))[0]
			}
			return loadDir(l.group.dir), true
		}
	case "o":
		if l.kind == lineFolder {
			m.status = runExplorer(`explorer.exe "` + l.group.dir + `"`)
		}
	case "a":
		if l.kind == lineFolder {
			return m.analyzePath(l.group.dir), true
		}
	default:
		return nil, false
	}
	return nil, true
}

// Lebar kolom daftar folder.
const (
	recentNameW  = 28
	recentCountW = 9  // "1.234 file"
	recentSizeW  = 10 // "1023.9 MB"
	recentTimeW  = 14 // "23 menit lalu"
)

// viewRecent menulis bagian "File baru" di bawah daftar drive, mengisi
// paling banyak h baris. Mengembalikan jumlah baris yang ditulis.
func (m *model) viewRecent(b *strings.Builder, h int) int {
	if h < 3 {
		return 0
	}
	title := titleStyle.Render("File baru") + dimStyle.Render(fmt.Sprintf(" · %d hari terakhir", m.days()))
	scanning := ""
	if m.recentRunning {
		scanning = fmt.Sprintf(" · memindai… %s file", format.Int(m.recentProg.Files.Load()))
	}
	switch {
	case m.recentRunning && m.recent == nil:
		m.line(b, title+dimStyle.Render(scanning))
		return 1
	case m.recentErr != nil && m.recent == nil:
		m.line(b, title+warnStyle.Render(" · gagal memindai: "+m.recentErr.Error()))
		return 1
	case m.recent == nil:
		return 0
	}
	// Data yang tampil bisa berasal dari hasil tersimpan; tunjukkan umurnya,
	// dan apakah perubahan sejak itu terus diikuti.
	status := scanning
	if status == "" {
		status = " · dipindai " + relTime(m.recentAt)
	}
	switch {
	case m.liveLost:
		status += warnStyle.Render(" · sebagian perubahan terlewat, r pindai ulang")
	case m.watcher != nil:
		status += newStyle.Render(" · ● langsung")
	}
	hint := " · tab pilih"
	if m.focusRecent {
		hint = " · tab kembali"
	}
	m.line(b, title+dimStyle.Render(status+hint))

	lines := m.recentLines()
	sel := selectableLines(lines)
	cur := -1
	if m.focusRecent && len(sel) > 0 {
		m.rcursor = min(max(m.rcursor, 0), len(sel)-1)
		cur = sel[m.rcursor]
	}
	listH := h - 1
	if cur >= 0 {
		// Saat menggulir ke atas, judul drive di atas baris pertamanya ikut terlihat.
		top := cur
		for top > 0 && !lines[top-1].selectable() {
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

	// Kolom lokasi mengisi sisa lebar layar.
	locW := min(max(m.screenWidth()-4-recentNameW-2-recentCountW-2-recentSizeW-2-recentTimeW-2, 10), 40)
	used := 1
	for i := m.roffset; i < min(m.roffset+listH, len(lines)); i++ {
		m.line(b, m.renderRecentLine(lines[i], i == cur, locW))
		used++
	}
	return used
}

func (m *model) renderRecentLine(l recentLine, selected bool, locW int) string {
	d := m.recent.Drives[l.drive]
	g := m.groups(l.drive)
	pick := func(s string) string {
		if selected {
			return selStyle.Render(s)
		}
		return dimStyle.Render(s)
	}
	switch l.kind {
	case lineDrive:
		var files int
		var size int64
		for _, f := range g.user {
			files += f.count
			size += f.size
		}
		text := " " + d.Root
		if di := m.driveOf(d.Root); di >= 0 && m.drives[di].Label != "" {
			text += "  " + m.drives[di].Label
		}
		info := fmt.Sprintf(" · %s folder · %s file baru · %s", format.Int(int64(len(g.user))), format.Int(int64(files)), format.Bytes(size))
		if !d.GrowthSince.IsZero() {
			info += fmt.Sprintf(" · ukuran drive %s sejak %s", signedBytes(d.Growth), d.GrowthSince.Format("2 Jan"))
		}
		return titleStyle.Render(dirStyle.Render(text)) + dimStyle.Render(info)
	case lineEmpty:
		return dimStyle.Render("    tidak ada file baru dari pengguna")
	case lineMore:
		groups := g.user
		if l.other {
			groups = g.other
		}
		rest := len(groups) - m.shownFor(pageKey(d.Root, l.other))
		return indent(l.other) + pick(fmt.Sprintf("▸ tampilkan %d lagi (%s folder tersisa)", min(rest, recentPage), format.Int(int64(rest))))
	case lineOther:
		arrow := "▸"
		if m.rexpand[d.Root] {
			arrow = "▾"
		}
		text := fmt.Sprintf("%s cache, aplikasi & sistem: %s folder · %s file · %s", arrow,
			format.Int(int64(len(g.other))), format.Int(int64(g.otherFiles)), format.Bytes(g.otherSize))
		return "    " + pick(text)
	}

	// Baris folder: nama folder di depan, lokasinya redup di kolom kedua.
	f := l.group
	name, loc := filepath.Base(f.dir), shortFolder(filepath.Dir(f.dir))
	switch rel := shortFolder(f.dir); {
	case rel == "folder pengguna":
		name, loc = rel, ""
	case filepath.Dir(f.dir) == f.dir:
		name, loc = f.dir, ""
	case rel == name:
		loc = "" // langsung di folder pengguna, misalnya Downloads
	}
	nameW := recentNameW
	if l.other {
		nameW -= 2
	}
	name = runewidth.FillRight(truncateMiddle(name, nameW), nameW)
	switch {
	case selected:
		name = selStyle.Render(name)
	case l.other:
		name = dimStyle.Render(name)
	default:
		name = dirStyle.Render(name)
	}
	if l.other && f.kind != "" {
		loc = string(f.kind) + " · " + loc
	}
	loc = runewidth.FillRight(truncateLeft(loc, locW), locW)
	count := fmt.Sprintf("%*s", recentCountW, format.Int(int64(f.count))+" file")
	size := fmt.Sprintf("%*s", recentSizeW, format.Bytes(f.size))
	return indent(l.other) + name + "  " + dimStyle.Render(loc) + "  " + dimStyle.Render(count) + "  " + size + "  " + dimStyle.Render(relTime(f.newest))
}

// indent adalah awal baris folder; bagian cache/aplikasi/sistem menjorok lebih dalam.
func indent(other bool) string {
	if other {
		return "      "
	}
	return "    "
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

func signedBytes(n int64) string {
	if n < 0 {
		return "-" + format.Bytes(-n)
	}
	return "+" + format.Bytes(n)
}
