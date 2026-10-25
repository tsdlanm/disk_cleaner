// Package recent mencari file yang baru ditambahkan ke setiap drive.
package recent

import (
	"context"
	"encoding/gob"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tsdlanm/cli_app/internal/sizer"
)

// Options mengatur pemindaian.
type Options struct {
	Days    int   // rentang "baru", dalam hari
	MinFile int64 // file di bawah ukuran ini tidak didaftar (tetap dihitung di Small)
	// GrowthDays adalah umur data pembanding ukuran drive sebelum diganti
	// (lihat DriveReport.Growth); bawaan sama dengan Days.
	GrowthDays int
	// Ignore berisi folder yang file-nya tidak pernah didaftar, misalnya
	// folder data program ini sendiri.
	Ignore []string
	// OnDir, jika diisi, dipanggil untuk setiap folder yang selesai dihitung,
	// agar hasilnya bisa dipakai ulang (misalnya mengisi cache ukuran folder).
	OnDir func(path string, inf sizer.Info)
}

// DefaultOptions dipakai jika konfigurasi tidak mengatur nilai.
var DefaultOptions = Options{Days: 7, MinFile: 1 << 20}

func (o Options) withDefaults() Options {
	if o.Days <= 0 {
		o.Days = DefaultOptions.Days
	}
	if o.MinFile <= 0 {
		o.MinFile = DefaultOptions.MinFile
	}
	if o.GrowthDays <= 0 {
		o.GrowthDays = o.Days
	}
	return o
}

// ignored melaporkan apakah path berada di salah satu folder Ignore.
func (o Options) ignored(path string) bool {
	for _, dir := range o.Ignore {
		if len(path) > len(dir) && strings.EqualFold(path[:len(dir)], dir) && os.IsPathSeparator(path[len(dir)]) {
			return true
		}
	}
	return false
}

// Root adalah satu drive yang dipindai.
type Root struct {
	Path string // "C:\"
	Used uint64 // ruang terpakai menurut Windows
}

// Kind menjelaskan asal sebuah file baru.
type Kind string

const (
	KindUser   Kind = ""         // file pengguna (Downloads, Documents, proyek, ...)
	KindSystem Kind = "sistem"   // Windows, ProgramData, Recycle Bin, ...
	KindApp    Kind = "aplikasi" // AppData, Program Files
	KindCache  Kind = "cache"    // cache, temp, node_modules, .git, ...
)

// File adalah satu file yang baru ditambahkan.
type File struct {
	Path    string
	Size    int64
	Created time.Time
	Kind    Kind
}

// DriveReport adalah hasil untuk satu drive.
type DriveReport struct {
	Root string
	// Files adalah semua file baru (termasuk buatan sistem dan aplikasi)
	// yang tidak lebih kecil dari Options.MinFile, urut dari yang paling baru.
	Files    []File
	FileSize int64
	// Small adalah file baru yang lebih kecil dari Options.MinFile; hanya
	// dihitung, tidak didaftar.
	SmallSize  int64
	SmallCount int64

	// Growth adalah pertambahan bersih ukuran drive sejak GrowthSince;
	// hanya ada jika pemindaian sebelumnya tersimpan.
	Growth      int64
	GrowthSince time.Time

	idx *index // untuk Apply; tidak ikut disimpan
}

// Report adalah hasil pemindaian semua drive.
type Report struct {
	Since  time.Time
	Drives []DriveReport
}

// Progress berisi penghitung yang aman dibaca selama Scan berjalan.
type Progress struct {
	Files atomic.Int64
	Bytes atomic.Int64
}

// Folder yang menentukan label asal file baru. kindAtRoot hanya berlaku
// untuk folder langsung di root drive; kindAnywhere berlaku di mana saja.
var (
	kindAtRoot = map[string]Kind{
		"windows": KindSystem, "programdata": KindSystem, "$recycle.bin": KindSystem,
		"system volume information": KindSystem, "recovery": KindSystem, "perflogs": KindSystem,
		"msocache": KindSystem, "deliveryoptimization": KindSystem, "windows.old": KindSystem,
		"$windows.~bt":  KindSystem,
		"program files": KindApp, "program files (x86)": KindApp,
	}
	kindAnywhere = map[string]Kind{
		"appdata":      KindApp,
		"node_modules": KindCache, ".git": KindCache, "__pycache__": KindCache, ".venv": KindCache,
		"cache": KindCache, "caches": KindCache, ".cache": KindCache, "temp": KindCache, "tmp": KindCache,
		"inetcache": KindCache, ".gradle": KindCache, ".m2": KindCache, ".nuget": KindCache,
		".npm": KindCache, "go-build": KindCache,
	}
)

// childKind menentukan label untuk folder name di dalam folder berlabel k.
// Label yang lebih spesifik (cache) menang atas label umum (aplikasi/sistem).
func childKind(k Kind, name string, atRoot bool) Kind {
	name = strings.ToLower(name)
	if c, ok := kindAnywhere[name]; ok && (k == KindUser || c == KindCache) {
		return c
	}
	if c, ok := kindAtRoot[name]; ok && atRoot {
		return c
	}
	// Folder bertitik (.vscode, .claude, .dart_tool, ...) berisi data alat dan
	// aplikasi, bukan file yang ditambahkan pengguna.
	if k == KindUser && strings.HasPrefix(name, ".") {
		return KindApp
	}
	return k
}

type scan struct {
	ctx   context.Context
	p     *Progress
	opt   Options
	since time.Time

	mu     sync.Mutex
	files  []File
	small  int64
	smallN int64
	denied int64
}

// Scan memindai roots. baseDir adalah folder untuk menyimpan ukuran drive
// sebagai pembanding; kosong berarti tanpa pembanding.
func Scan(ctx context.Context, roots []Root, opt Options, baseDir string, p *Progress) (*Report, error) {
	if p == nil {
		p = &Progress{}
	}
	opt = opt.withDefaults()
	now := time.Now()
	rep := &Report{Since: now.AddDate(0, 0, -opt.Days)}

	// Semua drive dipindai bersamaan agar selesai secepat mungkin; kuota
	// paralel tetap dibagi lewat sizer.Slots.
	rep.Drives = make([]DriveReport, len(roots))
	var wg sync.WaitGroup
	for i, root := range roots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &scan{ctx: ctx, p: p, opt: opt, since: rep.Since}
			size := s.walk(root.Path, true, KindUser).Size
			slices.SortFunc(s.files, func(a, b File) int { return b.Created.Compare(a.Created) })
			d := DriveReport{Root: root.Path, Files: s.files, SmallSize: s.small, SmallCount: s.smallN}
			for _, f := range s.files {
				d.FileSize += f.Size
			}
			if baseDir != "" && ctx.Err() == nil {
				d.Growth, d.GrowthSince = compare(baseDir, root.Path, size, opt.GrowthDays, now)
			}
			rep.Drives[i] = d
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return rep, nil
}

// walk menghitung ukuran path dan mencatat file baru di dalamnya. kind
// adalah label asal untuk file di folder ini. Hasilnya berformat sama dengan
// sizer.Info agar bisa dipakai ulang lewat Options.OnDir.
func (s *scan) walk(path string, atRoot bool, kind Kind) sizer.Info {
	if s.ctx.Err() != nil {
		return sizer.Info{}
	}
	entries, err := os.ReadDir(path)
	if err != nil && len(entries) == 0 {
		s.mu.Lock()
		s.denied++
		s.mu.Unlock()
		inf := sizer.Info{Denied: true, Locked: 1}
		s.report(path, inf)
		return inf
	}

	var (
		inf                sizer.Info
		mu                 sync.Mutex
		fileBytes, files   int64
		smallBytes, smallN int64
		found              []File
		wg                 sync.WaitGroup
	)
	merge := func(c sizer.Info) {
		mu.Lock()
		inf.Size += c.Size
		inf.Files += c.Files
		inf.Dirs += c.Dirs + 1
		inf.Locked += c.Locked
		mu.Unlock()
	}
	for _, e := range entries {
		if sizer.IsRealDir(e) {
			ck := childKind(kind, e.Name(), atRoot)
			child := filepath.Join(path, e.Name())
			select {
			case sizer.Slots <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sizer.Slots }()
					merge(s.walk(child, false, ck))
				}()
			default:
				merge(s.walk(child, false, ck))
			}
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files++
		fileBytes += fi.Size()
		created := creationTime(fi)
		if created.Before(s.since) {
			continue
		}
		if fi.Size() < s.opt.MinFile {
			smallBytes += fi.Size()
			smallN++
			continue
		}
		fp := filepath.Join(path, e.Name())
		if s.opt.ignored(fp) {
			continue
		}
		found = append(found, File{Path: fp, Size: fi.Size(), Created: created, Kind: kind})
	}
	s.p.Files.Add(files)
	s.p.Bytes.Add(fileBytes)
	wg.Wait()

	s.mu.Lock()
	s.files = append(s.files, found...)
	s.small += smallBytes
	s.smallN += smallN
	s.mu.Unlock()

	inf.Size += fileBytes
	inf.Files += files
	s.report(path, inf)
	return inf
}

func (s *scan) report(path string, inf sizer.Info) {
	if s.opt.OnDir != nil {
		s.opt.OnDir(path, inf)
	}
}

// creationTime mengembalikan waktu file dibuat di lokasinya (diunduh, disalin,
// diekstrak). Di Windows nilai ini ikut terbaca dari ReadDir tanpa syscall tambahan.
// Jika tanggal dibuat tidak masuk akal, waktu terakhir diubah yang dipakai.
func creationTime(fi fs.FileInfo) time.Time {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		if t, ok := fileTime(d.CreationTime); ok {
			return t
		}
	}
	return fi.ModTime()
}

// Tanggal di luar rentang ini dianggap tidak valid.
var minValidTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// fileTime mengubah FILETIME (satuan 100 ns sejak tahun 1601) menjadi waktu.
// Tidak memakai Filetime.Nanoseconds(), yang overflow untuk tanggal sangat
// lama: file hasil ekstrak zip bisa bertanggal dibuat 1601 (nilai kosong),
// yang lalu terbaca sebagai waktu acak di dekat "sekarang".
func fileTime(ft syscall.Filetime) (time.Time, bool) {
	const unixEpoch = 116444736000000000 // 1970-01-01 dalam satuan FILETIME
	v := uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
	if v < unixEpoch {
		return time.Time{}, false
	}
	t := time.Unix(0, int64((v-unixEpoch)*100))
	if t.Before(minValidTime) || t.After(time.Now().Add(24*time.Hour)) {
		return time.Time{}, false
	}
	return t, true
}

type baseline struct {
	Time time.Time
	Size int64
}

func baselinePath(baseDir, root string) string {
	name := strings.TrimRight(strings.ReplaceAll(root, `:`, ""), `\/`)
	return filepath.Join(baseDir, "size-"+name+".gob")
}

// compare mengembalikan pertambahan ukuran drive sejak pemindaian yang
// tersimpan, lalu memperbarui data itu jika sudah lebih lama dari rentang hari.
func compare(baseDir, root string, size int64, days int, now time.Time) (growth int64, since time.Time) {
	path := baselinePath(baseDir, root)
	var old baseline
	if f, err := os.Open(path); err == nil {
		if gob.NewDecoder(f).Decode(&old) != nil {
			old = baseline{}
		}
		f.Close()
	}
	if !old.Time.IsZero() {
		growth, since = size-old.Size, old.Time
	}
	if old.Time.IsZero() || now.Sub(old.Time) >= time.Duration(days)*24*time.Hour {
		_ = writeGob(path, baseline{Time: now, Size: size})
	}
	return growth, since
}
