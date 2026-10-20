// Package elevate meminta hak Administrator untuk program ini.
package elevate

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ErrDenied dikembalikan jika pengguna menolak prompt UAC.
var ErrDenied = errors.New("izin Administrator ditolak")

// IsAdmin melaporkan apakah proses ini berjalan dengan hak Administrator.
func IsAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// SudoInline melaporkan apakah sudo Windows aktif dalam mode inline, yaitu
// program elevated bisa berjalan di jendela terminal yang sama.
func SudoInline() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Sudo`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("Enabled")
	return err == nil && v == 3
}

// RunSudo menjalankan ulang program ini lewat sudo di terminal yang sama dan
// menunggu sampai selesai.
func RunSudo(args []string) (exitCode int, err error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	c := exec.Command("sudo.exe", append([]string{exe}, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl+C ditangani oleh proses elevated, bukan proses ini.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	err = c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

// Relaunch menjalankan ulang program ini sebagai Administrator lewat prompt
// UAC. Windows selalu membuka jendela console baru untuk proses elevated.
func Relaunch(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	err = windows.ShellExecute(0,
		windows.StringToUTF16Ptr("runas"),
		windows.StringToUTF16Ptr(exe),
		windows.StringToUTF16Ptr(strings.Join(quoted, " ")),
		windows.StringToUTF16Ptr(cwd),
		windows.SW_SHOWNORMAL)
	if errors.Is(err, windows.ERROR_CANCELLED) {
		return ErrDenied
	}
	return err
}

// EnableBackupPrivilege mengaktifkan SeBackupPrivilege agar folder yang
// dibatasi ACL tetap bisa dibaca isinya. Hanya berhasil jika proses elevated.
func EnableBackupPrivilege() error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeBackupPrivilege"), &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}
