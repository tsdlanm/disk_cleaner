package recent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func names(d DriveReport) []string {
	var out []string
	for _, f := range d.Files {
		out = append(out, filepath.Base(f.Path))
	}
	return out
}

func TestCheckApply(t *testing.T) {
	root := t.TempDir()
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
	opt := Options{Days: 7, MinFile: 1 << 20, Ignore: []string{filepath.Join(root, "data")}}

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
