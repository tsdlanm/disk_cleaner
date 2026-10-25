package recent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Change adalah keadaan terbaru sebuah path yang dilaporkan berubah.
type Change struct {
	Path string
	Gone bool  // path sudah tidak ada (dihapus atau diganti nama)
	File *File // terisi jika path sekarang adalah file baru yang masuk daftar
}

// Check membaca keadaan terbaru paths (hasil watch). Melakukan syscall per
// path, jadi panggil di luar goroutine UI.
func Check(paths []string, opt Options, now time.Time) []Change {
	opt = opt.withDefaults()
	since := now.AddDate(0, 0, -opt.Days)
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		c := Change{Path: p}
		fi, err := os.Lstat(p)
		switch {
		case err != nil:
			c.Gone = os.IsNotExist(err)
			if !c.Gone {
				continue // tidak terbaca (misalnya akses ditolak): biarkan apa adanya
			}
		case fi.Mode().IsRegular() && fi.Size() >= opt.MinFile && !opt.ignored(p):
			if created := creationTime(fi); !created.Before(since) {
				c.File = &File{Path: p, Size: fi.Size(), Created: created, Kind: KindOf(p)}
			}
		}
		out = append(out, c)
	}
	return out
}

// KindOf menentukan label asal sebuah file dari foldernya.
func KindOf(path string) Kind {
	dir := filepath.Dir(path)
	rest := strings.TrimPrefix(dir, filepath.VolumeName(dir))
	k := KindUser
	prev := ""
	for i, name := range strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' }) {
		k = childKind(k, name, i == 0)
		if cachePair[[2]string{prev, strings.ToLower(name)}] {
			k = KindCache
		}
		prev = strings.ToLower(name)
	}
	return k
}

// cachePair adalah pasangan folder induk-anak yang berisi cache unduhan atau
// hasil build sementara, misalnya modul Go (go\pkg\mod) dan build Android/Gradle.
var cachePair = map[[2]string]bool{
	{"pkg", "mod"}:             true,
	{"build", "intermediates"}: true,
	{"build", "tmp"}:           true,
}

// index mempercepat pencarian di DriveReport.Files saat menerapkan perubahan.
type index struct {
	files map[string]bool
	dirs  map[string]int // folder induk (semua tingkat) -> jumlah file di dalamnya
}

func (d *DriveReport) index() *index {
	if d.idx == nil {
		d.idx = &index{files: make(map[string]bool, len(d.Files)), dirs: make(map[string]int)}
		for _, f := range d.Files {
			d.idx.add(f.Path)
		}
	}
	return d.idx
}

func (x *index) add(path string) {
	x.files[path] = true
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		x.dirs[p]++
		if filepath.Dir(p) == p {
			break
		}
	}
}

func (x *index) remove(path string) {
	delete(x.files, path)
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		if x.dirs[p]--; x.dirs[p] <= 0 {
			delete(x.dirs, p)
		}
		if filepath.Dir(p) == p {
			break
		}
	}
}

// Apply menerapkan perubahan ke laporan: file baru masuk ke posisinya
// (urut dari yang terbaru), ukuran file yang tumbuh diperbarui, dan file atau
// folder yang hilang dibuang dari daftar. Mengembalikan true jika ada yang berubah.
func (r *Report) Apply(changes []Change) bool {
	changed := false
	for _, c := range changes {
		d := r.drive(c.Path)
		if d == nil {
			continue
		}
		x := d.index()
		switch {
		case c.File != nil:
			changed = d.upsert(x, *c.File) || changed
		case x.files[c.Path]:
			changed = d.removeIf(x, func(f File) bool { return f.Path == c.Path }) || changed
		case c.Gone && x.dirs[c.Path] > 0:
			// Folder yang dihapus/dipindah: semua file di dalamnya ikut hilang.
			prefix := c.Path + string(filepath.Separator)
			changed = d.removeIf(x, func(f File) bool { return strings.HasPrefix(f.Path, prefix) }) || changed
		}
	}
	return changed
}

func (r *Report) drive(path string) *DriveReport {
	vol := filepath.VolumeName(path)
	for i := range r.Drives {
		if strings.EqualFold(filepath.VolumeName(r.Drives[i].Root), vol) {
			return &r.Drives[i]
		}
	}
	return nil
}

func (d *DriveReport) upsert(x *index, f File) bool {
	if x.files[f.Path] {
		i := slices.IndexFunc(d.Files, func(g File) bool { return g.Path == f.Path })
		if d.Files[i] == f {
			return false
		}
		d.FileSize += f.Size - d.Files[i].Size
		if d.Files[i].Created.Equal(f.Created) {
			d.Files[i] = f
			return true
		}
		d.Files = slices.Delete(d.Files, i, i+1)
		d.FileSize -= f.Size
		x.remove(f.Path)
	}
	// Daftar urut dari yang terbaru; file baru biasanya masuk di depan.
	i, _ := slices.BinarySearchFunc(d.Files, f, func(a, b File) int { return b.Created.Compare(a.Created) })
	d.Files = slices.Insert(d.Files, i, f)
	d.FileSize += f.Size
	x.add(f.Path)
	return true
}

func (d *DriveReport) removeIf(x *index, drop func(File) bool) bool {
	n := len(d.Files)
	d.Files = slices.DeleteFunc(d.Files, func(f File) bool {
		if !drop(f) {
			return false
		}
		d.FileSize -= f.Size
		x.remove(f.Path)
		return true
	})
	return len(d.Files) != n
}

// Clone menyalin laporan agar bisa disimpan di goroutine lain sementara
// aslinya terus diperbarui.
func (r *Report) Clone() *Report {
	c := &Report{Since: r.Since, Drives: make([]DriveReport, len(r.Drives))}
	for i, d := range r.Drives {
		d.Files = slices.Clone(d.Files)
		d.idx = nil
		c.Drives[i] = d
	}
	return c
}
