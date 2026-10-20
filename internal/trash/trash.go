// Package trash memindahkan file dan folder ke Recycle Bin Windows.
package trash

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procSHFileOperationW = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// shFileOpStruct mengikuti layout SHFILEOPSTRUCTW (64-bit).
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

const (
	foDelete           = 0x3
	fofSilent          = 0x4    // tanpa jendela progress
	fofNoConfirmation  = 0x10   // konfirmasi sudah dilakukan di TUI
	fofAllowUndo       = 0x40   // ke Recycle Bin, bukan hapus permanen
	fofNoErrorUI       = 0x400  // error dilaporkan lewat nilai kembali
	fofWantNukeWarning = 0x4000 // tetap tanya jika item terpaksa dihapus permanen
)

// Move memindahkan paths (path absolut) ke Recycle Bin. Jika item tidak bisa
// masuk Recycle Bin (misalnya terlalu besar), Windows menampilkan dialog
// konfirmasi sebelum menghapusnya permanen.
func Move(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	// Operasi shell sebaiknya berjalan di thread COM single-threaded.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil {
		defer windows.CoUninitialize()
	}

	// pFrom: daftar path dipisah NUL, diakhiri NUL ganda.
	var from []uint16
	for _, p := range paths {
		u, err := windows.UTF16FromString(p)
		if err != nil {
			return err
		}
		from = append(from, u...)
	}
	from = append(from, 0)

	op := shFileOpStruct{
		wFunc:  foDelete,
		pFrom:  &from[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI | fofWantNukeWarning,
	}
	r, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 {
		return fmt.Errorf("Windows menolak penghapusan (kode %#x)", r)
	}
	if op.fAnyOperationsAborted != 0 {
		return errors.New("penghapusan dibatalkan")
	}
	return nil
}
