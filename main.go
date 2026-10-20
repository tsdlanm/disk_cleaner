package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/tsdlanm/cli_app/internal/elevate"
	"github.com/tsdlanm/cli_app/internal/ui"
)

// pathFlag membuka TUI langsung di folder tertentu. Dipakai saat restart
// sebagai Administrator agar pengguna tetap di folder yang sama.
const pathFlag = "--path"

func main() {
	opts := ui.Options{Admin: elevate.IsAdmin(), StartPath: argValue(pathFlag)}
	if opts.Admin {
		// Best effort: tanpa privilege ini program tetap jalan seperti biasa.
		_ = elevate.EnableBackupPrivilege()
	}

	for {
		res, err := ui.Run(opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		if !res.Elevate {
			return
		}

		var args []string
		if res.Path != "" {
			args = []string{pathFlag, res.Path}
		}
		if elevate.SudoInline() {
			// sudo inline: TUI elevated berjalan di terminal yang sama.
			if code, err := elevate.RunSudo(args); err == nil && code == 0 {
				return
			}
			opts.Status = "sudo gagal atau ditolak; tetap berjalan tanpa hak Administrator"
		} else {
			err := elevate.Relaunch(args)
			if err == nil {
				// TUI berlanjut di jendela Administrator yang baru.
				return
			}
			opts.Status = "Gagal menjalankan sebagai Administrator: " + err.Error()
			if errors.Is(err, elevate.ErrDenied) {
				opts.Status = "Izin Administrator ditolak"
			}
		}
		// Elevasi gagal: buka lagi TUI biasa di folder yang sama.
		opts.StartPath = res.Path
	}
}

// argValue mengembalikan nilai argumen "name value", atau "" jika tidak ada.
func argValue(name string) string {
	for i := 1; i+1 < len(os.Args); i++ {
		if os.Args[i] == name {
			return os.Args[i+1]
		}
	}
	return ""
}
