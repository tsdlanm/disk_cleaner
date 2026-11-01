package recent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestKindOf(t *testing.T) {
	for path, want := range map[string]Kind{
		`C:\Users\a\Downloads\x.iso`:               KindUser,
		`C:\Windows\Temp\x.log`:                    KindCache,
		`C:\Windows\System32\x.dll`:                KindSystem,
		`C:\Users\a\AppData\Local\App\x.db`:        KindApp,
		`C:\Users\a\AppData\Local\Google\Cache\x`:  KindCache,
		`D:\proyek\node_modules\x.js`:              KindCache,
		`D:\data\windows\x.bin`:                    KindUser, // "windows" hanya sistem di root drive
		`C:\Program Files\App\x.exe`:               KindApp,
		`C:\x.bin`:                                 KindUser,
		`C:\Users\a\.vscode\extensions\x.vsix`:     KindApp,
		`D:\proyek\.git\objects\x`:                 KindCache,
		`C:\Users\a\go\pkg\mod\golang.org\x\a.zip`: KindCache,
		`D:\app\build\intermediates\dex\a.dex`:     KindCache,
		`D:\app\build\outputs\apk\a.apk`:           KindUser,
	} {
		if got := KindOf(path); got != want {
			t.Errorf("KindOf(%s) = %q, want %q", path, got, want)
		}
	}
}

// tempDir membuat folder uji berejaan panjang, seperti drive yang dipindai
// dari C:\ (t.TempDir mengikuti TEMP yang bisa berejaan pendek 8.3).
func tempDir(t *testing.T) string { return LongPath(t.TempDir()) }

// shortSpelling mengubah path yang ada ke ejaan pendek 8.3, seperti yang
// dilaporkan Windows untuk proses yang memakai TEMP. Tanpa nama pendek
// (8.3 dimatikan di drive), path dikembalikan apa adanya.
func shortSpelling(t *testing.T, path string) string {
	t.Helper()
	u, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(u, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		return path
	}
	return windows.UTF16ToString(buf[:n])
}

// File dilaporkan dengan ejaan berbeda saat dibuat dan saat dihapus: tetap
// dikenali sebagai file yang sama, jadi tidak dobel dan hilang saat dihapus.
func TestCheckSpelling(t *testing.T) {
	root := tempDir(t)
	dir := filepath.Join(root, "go-build-contoh-panjang", "b001")
	file := filepath.Join(dir, "pkg.a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, make([]byte, 2<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	// Seperti proses yang memakai TEMP: hanya awalan path yang berejaan pendek,
	// nama folder di bawahnya (dari isi folder) tetap panjang.
	short := filepath.Join(shortSpelling(t, root), "go-build-contoh-panjang", "b001", "pkg.a")
	opt := Options{MinFile: 1 << 20}
	now := time.Now()

	// Dibuat lewat ejaan pendek: tercatat dengan ejaan panjang.
	r := &Report{Drives: []DriveReport{{Root: filepath.VolumeName(root) + `\`}}}
	cs := Check([]string{short}, opt, now)
	if len(cs) != 1 || cs[0].File == nil || cs[0].Path != file {
		t.Fatalf("Check(%s) = %+v, want path %s", short, cs, file)
	}
	r.Apply(cs)
	// Dilaporkan lagi lewat ejaan panjang: file yang sama, tidak dobel.
	r.Apply(Check([]string{file}, opt, now))
	if got := names(r.Drives[0]); len(got) != 1 {
		t.Fatalf("Files = %v, want satu file", got)
	}

	// Dihapus, dilaporkan lewat ejaan pendek: hilang dari daftar.
	if err := os.RemoveAll(filepath.Join(root, "go-build-contoh-panjang")); err != nil {
		t.Fatal(err)
	}
	cs = Check([]string{short, filepath.Dir(short)}, opt, now)
	if len(cs) != 2 || !cs[0].Gone || cs[0].Path != file {
		t.Fatalf("setelah dihapus: %+v, want Gone dan path %s", cs, file)
	}
	if !r.Apply(cs) || len(r.Drives[0].Files) != 0 {
		t.Errorf("file yang dihapus lewat ejaan lain tidak hilang: %v", names(r.Drives[0]))
	}
}

// Normalize menyatukan hasil tersimpan lama yang memuat satu file dengan dua ejaan.
func TestReportNormalize(t *testing.T) {
	root := tempDir(t)
	file := filepath.Join(root, "dokumen-dengan-nama-panjang", "a.bin")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	r := &Report{Drives: []DriveReport{{Root: filepath.VolumeName(root) + `\`, FileSize: 20, Files: []File{
		{Path: shortSpelling(t, file), Size: 10, Created: at},
		{Path: file, Size: 10, Created: at},
	}}}}
	if !r.Normalize() && shortSpelling(t, file) != file {
		t.Error("Normalize tidak melaporkan perubahan")
	}
	if d := r.Drives[0]; len(d.Files) != 1 || d.Files[0].Path != file || d.FileSize != 10 {
		t.Errorf("setelah Normalize: %+v", d)
	}
}

// File yang sudah tidak ada (dihapus selagi program tertutup, atau file
// sementara) tidak ikut tampil saat hasil tersimpan dimuat.
func TestReportPruneMissing(t *testing.T) {
	root := tempDir(t)
	ada, hilang := filepath.Join(root, "ada.bin"), filepath.Join(root, "sementara", "pkg.a")
	if err := os.WriteFile(ada, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	r := &Report{Drives: []DriveReport{{Root: filepath.VolumeName(root) + `\`, FileSize: 30, Files: []File{
		{Path: hilang, Size: 20, Created: at},
		{Path: ada, Size: 10, Created: at},
	}}}}
	if !r.PruneMissing() {
		t.Fatal("PruneMissing tidak melaporkan perubahan")
	}
	if d := r.Drives[0]; len(d.Files) != 1 || d.Files[0].Path != ada || d.FileSize != 10 {
		t.Errorf("setelah PruneMissing: %+v", d)
	}
	if r.PruneMissing() {
		t.Error("PruneMissing kedua masih melaporkan perubahan")
	}
}

func names(d DriveReport) []string {
	var out []string
	for _, f := range d.Files {
		out = append(out, filepath.Base(f.Path))
	}
	return out
}

func TestCheckApply(t *testing.T) {
	root := tempDir(t)
	vol := filepath.VolumeName(root) + `\`
	now := time.Now()
	old := File{Path: filepath.Join(root, "lama", "a.bin"), Size: 5 << 20, Created: now.Add(-2 * time.Hour)}
	rep := &Report{Drives: []DriveReport{{Root: vol, Files: []File{old}, FileSize: old.Size}}}

	big := filepath.Join(root, "dl", "big.iso")
	small := filepath.Join(root, "dl", "kecil.txt")
	ign := filepath.Join(root, "data", "snapshot.gob")
	for p, size := range map[string]int{big: 2 << 20, small: 10, ign: 2 << 20} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opt := Options{MinFile: 1 << 20, Ignore: []string{filepath.Join(root, "data")}}

	// File besar masuk di depan; file kecil dan folder yang diabaikan tidak.
	if !rep.Apply(Check([]string{big, small, ign}, opt, now)) {
		t.Fatal("Apply tidak melaporkan perubahan")
	}
	d := rep.Drives[0]
	if got := names(d); len(got) != 2 || got[0] != "big.iso" || got[1] != "a.bin" {
		t.Fatalf("Files = %v, want [big.iso a.bin]", got)
	}
	if d.FileSize != old.Size+2<<20 {
		t.Errorf("FileSize = %d", d.FileSize)
	}

	// File yang tumbuh diperbarui ukurannya, tanpa duplikat.
	if err := os.WriteFile(big, make([]byte, 3<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	rep.Apply(Check([]string{big}, opt, now))
	if d := rep.Drives[0]; len(d.Files) != 2 || d.Files[0].Size != 3<<20 || d.FileSize != old.Size+3<<20 {
		t.Errorf("setelah tumbuh: %+v", d)
	}

	// Perubahan yang sama tidak dihitung sebagai perubahan.
	if rep.Apply(Check([]string{big}, opt, now)) {
		t.Error("Apply tanpa perubahan melaporkan perubahan")
	}

	// File dihapus: hilang dari daftar.
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	rep.Apply(Check([]string{big}, opt, now))
	if got := names(rep.Drives[0]); len(got) != 1 || got[0] != "a.bin" {
		t.Errorf("setelah hapus: %v", got)
	}

	// Folder berisi file di daftar dihapus: semua isinya hilang dari daftar.
	rep.Apply(Check([]string{filepath.Join(root, "lama")}, opt, now))
	if d := rep.Drives[0]; len(d.Files) != 0 || d.FileSize != 0 {
		t.Errorf("setelah folder hilang: %+v", d)
	}
}

func TestClone(t *testing.T) {
	rep := &Report{Drives: []DriveReport{{Root: `C:\`, Files: []File{{Path: `C:\a`}}}}}
	c := rep.Clone()
	rep.Drives[0].Files[0].Path = `C:\b`
	if c.Drives[0].Files[0].Path != `C:\a` {
		t.Error("Clone berbagi slice Files dengan aslinya")
	}
}
