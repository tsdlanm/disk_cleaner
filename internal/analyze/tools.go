package analyze

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

// Alat yang bisa dipanggil AI saat menjawab. Semuanya hanya membaca nama,
// ukuran, dan tanggal; isi file tidak pernah dibaca, dan tidak ada yang bisa
// mengubah atau menghapus apa pun. Semua path dibatasi di dalam folder yang
// dianalisis.
const (
	toolListDir   = "list_dir"
	toolFindFiles = "find_files"
)

const (
	maxListRows    = 40
	maxFindResults = 40
	maxFindVisited = 400000
	maxFindMatches = 5000
	maxToolChars   = 6000
	toolTimeout    = 25 * time.Second
)

// Toolbox menjalankan alat untuk satu percakapan.
type Toolbox struct {
	Root    string
	Reg     *Registry
	measure func(string) sizer.Info
}

var defaultSizer = sync.OnceValue(sizer.New)

// NewToolbox membuat Toolbox untuk folder root. measure menghitung ukuran
// folder (biasanya Sizer milik aplikasi, agar hasil yang sudah ada dipakai
// ulang); nil memakai penghitung bawaan.
func NewToolbox(root string, reg *Registry, measure func(string) sizer.Info) *Toolbox {
	if measure == nil {
		measure = func(p string) sizer.Info { return defaultSizer().Measure(p) }
	}
	return &Toolbox{Root: filepath.Clean(root), Reg: reg, measure: measure}
}

// Defs menjelaskan alat kepada AI.
func (t *Toolbox) Defs() []ToolDef {
	where := map[string]any{
		"id":   map[string]any{"type": "integer", "description": "Nomor [id] folder dari ringkasan atau hasil alat sebelumnya."},
		"path": map[string]any{"type": "string", "description": "Path folder (absolut, atau relatif terhadap folder yang dianalisis). Kosong berarti folder yang dianalisis."},
	}
	return []ToolDef{
		{
			Name: toolListDir,
			Description: "Melihat isi satu folder: nama, ukuran, jumlah file, dan tanggal ubah setiap item, dari yang terbesar. " +
				"Pakai untuk folder yang tidak dirinci di ringkasan atau untuk melihat item kecil.",
			Parameters: map[string]any{"type": "object", "properties": where},
		},
		{
			Name: toolFindFiles,
			Description: "Mencari file di dalam satu folder (semua tingkat) menurut ukuran, umur, atau nama. " +
				"Pakai untuk pertanyaan seperti 'file besar yang sudah lama tidak diubah'. Mengembalikan yang terbesar dulu.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"id":              where["id"],
				"path":            where["path"],
				"min_size_mb":     map[string]any{"type": "number", "description": "Ukuran minimal file dalam MB."},
				"older_than_days": map[string]any{"type": "number", "description": "Hanya file yang tidak diubah selama ini (hari) atau lebih."},
				"name_contains":   map[string]any{"type": "string", "description": "Nama file memuat teks ini (tanpa membedakan huruf besar/kecil)."},
				"limit":           map[string]any{"type": "integer", "description": "Jumlah hasil maksimal (bawaan 20, paling banyak 40)."},
			}},
		},
	}
}

type toolArgs struct {
	ID            int     `json:"id"`
	Path          string  `json:"path"`
	MinSizeMB     float64 `json:"min_size_mb"`
	OlderThanDays float64 `json:"older_than_days"`
	NameContains  string  `json:"name_contains"`
	Limit         int     `json:"limit"`
}

func parseArgs(raw string) (toolArgs, error) {
	var a toolArgs
	if strings.TrimSpace(raw) == "" {
		return a, nil
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return a, fmt.Errorf("argumen alat bukan JSON yang valid: %w", err)
	}
	return a, nil
}

// within melaporkan apakah path sama dengan root atau berada di dalamnya.
func within(path, root string) bool {
	root = strings.TrimRight(root, `\/`)
	path = strings.TrimRight(path, `\/`)
	if strings.EqualFold(path, root) {
		return true
	}
	return len(path) > len(root) && strings.EqualFold(path[:len(root)], root) && (path[len(root)] == '\\' || path[len(root)] == '/')
}

// resolve menentukan folder yang dimaksud, dan menolak yang di luar root.
func (t *Toolbox) resolve(a toolArgs) (string, error) {
	path := a.Path
	switch {
	case a.ID > 0:
		it, ok := t.Reg.Get(a.ID)
		if !ok {
			return "", fmt.Errorf("id %d tidak dikenal", a.ID)
		}
		path = it.Path
	case strings.TrimSpace(path) == "":
		path = t.Root
	case !filepath.IsAbs(path):
		path = filepath.Join(t.Root, path)
	}
	path = filepath.Clean(path)
	if !within(path, t.Root) {
		return "", fmt.Errorf("%s di luar folder yang dianalisis (%s); alat hanya boleh membaca di dalamnya", path, t.Root)
	}
	return path, nil
}

// rel menulis path relatif terhadap root untuk ditampilkan.
func (t *Toolbox) rel(path string) string {
	r, err := filepath.Rel(t.Root, path)
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return path
	}
	return r
}

// Describe menyusun kalimat untuk pengguna tentang alat yang dipanggil.
func (t *Toolbox) Describe(name, rawArgs string) string {
	a, err := parseArgs(rawArgs)
	if err != nil {
		return "Menjalankan alat " + name
	}
	where := "folder ini"
	if p, err := t.resolve(a); err == nil && !strings.EqualFold(p, t.Root) {
		where = t.rel(p)
	} else if err != nil {
		where = strings.TrimSpace(a.Path)
		if where == "" {
			where = fmt.Sprintf("id %d", a.ID)
		}
	}
	switch name {
	case toolListDir:
		return "Melihat isi " + where
	case toolFindFiles:
		var c []string
		if a.MinSizeMB > 0 {
			c = append(c, fmt.Sprintf("≥ %s MB", trimFloat(a.MinSizeMB)))
		}
		if a.OlderThanDays > 0 {
			c = append(c, fmt.Sprintf("tidak diubah ≥ %s hari", trimFloat(a.OlderThanDays)))
		}
		if a.NameContains != "" {
			c = append(c, fmt.Sprintf("nama memuat %q", a.NameContains))
		}
		label := "Mencari file di " + where
		if len(c) > 0 {
			label += " (" + strings.Join(c, ", ") + ")"
		}
		return label
	}
	return "Menjalankan alat " + name
}

func trimFloat(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", f), "0"), ".")
}

// Run menjalankan alat. Kegagalan dikembalikan sebagai teks hasil (bukan
// error), supaya AI bisa menyesuaikan diri. summary adalah ringkasan satu
// baris untuk pengguna.
func (t *Toolbox) Run(ctx context.Context, name, rawArgs string) (out, summary string) {
	a, err := parseArgs(rawArgs)
	if err == nil {
		switch name {
		case toolListDir:
			out, summary, err = t.listDir(ctx, a)
		case toolFindFiles:
			out, summary, err = t.findFiles(ctx, a)
		default:
			err = fmt.Errorf("alat %q tidak dikenal", name)
		}
	}
	if err != nil {
		return "Gagal: " + err.Error(), "gagal: " + err.Error()
	}
	if len(out) > maxToolChars {
		out = strings.ToValidUTF8(out[:maxToolChars], "") + "\n… hasil dipotong"
	}
	return out, summary
}

// measureCtx menghitung ukuran folder, atau berhenti menunggu saat ctx habis
// (perhitungan tetap berjalan dan mengisi cache untuk nanti).
func (t *Toolbox) measureCtx(ctx context.Context, path string) (sizer.Info, bool) {
	ch := make(chan sizer.Info, 1)
	go func() { ch <- t.measure(path) }()
	select {
	case inf := <-ch:
		return inf, true
	case <-ctx.Done():
		return sizer.Info{}, false
	}
}

func isLink(m fs.FileMode) bool { return m&(fs.ModeSymlink|fs.ModeIrregular) != 0 }

func (t *Toolbox) listDir(ctx context.Context, a toolArgs) (string, string, error) {
	dir, err := t.resolve(a)
	if err != nil {
		return "", "", err
	}
	fi, err := os.Lstat(dir)
	switch {
	case err != nil:
		return "", "", fmt.Errorf("tidak bisa membuka %s: %v", dir, errors.Unwrap(err))
	case !fi.IsDir():
		return "", "", fmt.Errorf("%s bukan folder", dir)
	case isLink(fi.Mode()):
		return "", "", fmt.Errorf("%s adalah tautan (junction); tidak dibuka", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil && len(entries) == 0 {
		return "", "", fmt.Errorf("tidak bisa membaca %s (akses ditolak?)", dir)
	}

	type row struct {
		name         string
		path         string
		isDir        bool
		size, files  int64
		mod          time.Time
		partial      bool
		link, denied bool
	}
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	rows := make([]row, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, e := range entries {
		r := row{name: e.Name(), path: filepath.Join(dir, e.Name())}
		if info, err := e.Info(); err == nil {
			r.mod = info.ModTime()
			if !e.IsDir() {
				r.size = info.Size()
			}
		}
		switch {
		case sizer.IsRealDir(e):
			r.isDir = true
		case isLink(e.Type()):
			r.link = true
		}
		rows[i] = r
		if r.isDir {
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				inf, ok := t.measureCtx(ctx, r.path)
				rows[i].size, rows[i].files, rows[i].partial, rows[i].denied = inf.Size, inf.Files, !ok, inf.Denied
			}()
		}
	}
	wg.Wait()

	slices.SortStableFunc(rows, func(x, y row) int {
		if c := cmp.Compare(y.size, x.size); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(x.name), strings.ToLower(y.name))
	})
	var total int64
	for _, r := range rows {
		total += r.size
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Isi %s · %s item · %s\n", dir, format.Int(int64(len(rows))), format.Bytes(total))
	for i, r := range rows {
		if i >= maxListRows {
			var rest int64
			for _, x := range rows[i:] {
				rest += x.size
			}
			fmt.Fprintf(&b, "… %s item lainnya · %s\n", format.Int(int64(len(rows)-i)), format.Bytes(rest))
			break
		}
		id := t.Reg.Add(r.path, r.isDir, r.size)
		name := r.name
		if r.isDir {
			name += `\`
		}
		switch {
		case r.link:
			fmt.Fprintf(&b, "[%d] %s · tautan (tidak dihitung)\n", id, name)
		case r.isDir && r.denied:
			fmt.Fprintf(&b, "[%d] %s · terkunci (tidak bisa dibaca)\n", id, name)
		case r.isDir && r.partial:
			fmt.Fprintf(&b, "[%d] %s · ≥ %s (belum selesai dihitung)\n", id, name, format.Bytes(r.size))
		case r.isDir:
			fmt.Fprintf(&b, "[%d] %s · %s · %s file · diubah %s\n", id, name, format.Bytes(r.size), format.Int(r.files), r.mod.Format("2006-01-02"))
		default:
			fmt.Fprintf(&b, "[%d] %s · %s · diubah %s\n", id, name, format.Bytes(r.size), r.mod.Format("2006-01-02"))
		}
	}
	return b.String(), fmt.Sprintf("%s item · %s", format.Int(int64(len(rows))), format.Bytes(total)), nil
}

func (t *Toolbox) findFiles(ctx context.Context, a toolArgs) (string, string, error) {
	base, err := t.resolve(a)
	if err != nil {
		return "", "", err
	}
	if fi, err := os.Lstat(base); err != nil || !fi.IsDir() || isLink(fi.Mode()) {
		return "", "", fmt.Errorf("%s bukan folder yang bisa dicari", base)
	}
	minSize := int64(a.MinSizeMB * (1 << 20))
	if a.MinSizeMB <= 0 && a.OlderThanDays <= 0 && a.NameContains == "" {
		minSize = 50 << 20 // tanpa kriteria: cukup file yang besar
	}
	limit := min(max(a.Limit, 1), maxFindResults)
	if a.Limit <= 0 {
		limit = 20
	}
	oldest := time.Now().Add(-time.Duration(a.OlderThanDays * 24 * float64(time.Hour)))
	needle := strings.ToLower(a.NameContains)

	type hit struct {
		path string
		size int64
		mod  time.Time
	}
	var hits []hit
	var matched int64
	visited, stopped := 0, ""
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // folder terkunci dilewati
		}
		if visited++; visited > maxFindVisited {
			stopped = "pencarian dihentikan setelah memeriksa " + format.Int(maxFindVisited) + " item"
			return filepath.SkipAll
		}
		if visited%512 == 0 && ctx.Err() != nil {
			stopped = "pencarian dihentikan karena kehabisan waktu"
			return filepath.SkipAll
		}
		if d.IsDir() || isLink(d.Type()) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() < minSize {
			return nil
		}
		if a.OlderThanDays > 0 && info.ModTime().After(oldest) {
			return nil
		}
		if needle != "" && !strings.Contains(strings.ToLower(d.Name()), needle) {
			return nil
		}
		matched += info.Size()
		hits = append(hits, hit{p, info.Size(), info.ModTime()})
		if len(hits) >= maxFindMatches {
			stopped = "pencarian dihentikan setelah " + format.Int(maxFindMatches) + " file cocok"
			return filepath.SkipAll
		}
		return nil
	})
	slices.SortStableFunc(hits, func(x, y hit) int { return cmp.Compare(y.size, x.size) })

	var b strings.Builder
	fmt.Fprintf(&b, "Ditemukan %s file · %s di %s", format.Int(int64(len(hits))), format.Bytes(matched), base)
	if stopped != "" {
		fmt.Fprintf(&b, " (%s)", stopped)
	}
	b.WriteString("\n")
	for _, h := range hits[:min(len(hits), limit)] {
		id := t.Reg.Add(h.path, false, h.size)
		rel, _ := filepath.Rel(base, h.path)
		fmt.Fprintf(&b, "[%d] %s · %s · diubah %s\n", id, rel, format.Bytes(h.size), h.mod.Format("2006-01-02"))
	}
	if len(hits) > limit {
		fmt.Fprintf(&b, "… %s file lainnya\n", format.Int(int64(len(hits)-limit)))
	}
	return b.String(), fmt.Sprintf("%s file · %s", format.Int(int64(len(hits))), format.Bytes(matched)), nil
}
