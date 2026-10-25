// Package watch memantau perubahan file di seluruh drive memakai
// ReadDirectoryChangesW. Tidak butuh hak Administrator dan tidak memindai
// apa pun: Windows yang memberi tahu setiap kali file dibuat, diubah
// ukurannya, diganti nama, atau dihapus.
package watch

import (
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Batch adalah kumpulan path yang berubah sejak batch sebelumnya, tanpa
// duplikat. Jenis perubahannya tidak disertakan: penerima cukup memeriksa
// keadaan path itu sekarang.
type Batch struct {
	Paths []string
	// Lost bernilai true jika sebagian perubahan terlewat (terlalu banyak
	// perubahan sekaligus), sehingga data turunan perlu dipindai ulang.
	Lost bool
}

// Interval adalah jeda minimal antar batch, agar perubahan beruntun (file
// yang sedang diunduh, misalnya) cukup diperiksa sekali.
var Interval = time.Second

// maxPending membatasi jumlah path yang ditampung di antara dua batch.
const maxPending = 20000

const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME | windows.FILE_NOTIFY_CHANGE_SIZE

// Watcher memantau beberapa drive/folder sekaligus.
type Watcher struct {
	// C menerima batch perubahan; ditutup setelah Close.
	C <-chan Batch

	out  chan Batch
	stop windows.Handle // event: sinyal berhenti untuk semua goroutine
	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once

	mu      sync.Mutex
	pending map[string]struct{}
	lost    bool
}

// Start mulai memantau roots (beserta seluruh isinya). Root yang tidak bisa
// dibuka dilewati; error hanya jika tidak ada satu pun yang bisa dipantau.
func Start(roots []string) (*Watcher, error) {
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	w := &Watcher{out: make(chan Batch), stop: stop, done: make(chan struct{}), pending: make(map[string]struct{})}
	w.C = w.out
	started := 0
	for _, root := range roots {
		h, ev, e := open(root)
		if e != nil {
			err = e
			continue
		}
		started++
		w.wg.Add(1)
		go w.watch(root, h, ev)
	}
	if started == 0 {
		windows.CloseHandle(stop)
		return nil, err
	}
	go w.flush()
	return w, nil
}

func open(root string) (h, ev windows.Handle, err error) {
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return 0, 0, err
	}
	h, err = windows.CreateFile(p, windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return 0, 0, err
	}
	ev, err = windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return 0, 0, err
	}
	return h, ev, nil
}

// Close menghentikan pemantauan dan menutup C.
func (w *Watcher) Close() {
	w.once.Do(func() {
		windows.SetEvent(w.stop)
		w.wg.Wait()
		close(w.done)
		windows.CloseHandle(w.stop)
	})
}

// watch membaca perubahan di root sampai Close dipanggil.
func (w *Watcher) watch(root string, h, ev windows.Handle) {
	defer w.wg.Done()
	defer windows.CloseHandle(h)
	defer windows.CloseHandle(ev)
	// Buffer harus selaras 4 byte; slice []uint32 menjamin itu.
	raw := make([]uint32, 32<<10)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&raw[0])), len(raw)*4)
	ov := new(windows.Overlapped)
	ov.HEvent = ev
	for {
		if err := windows.ReadDirectoryChanges(h, &buf[0], uint32(len(buf)), true, mask, nil, ov, 0); err != nil {
			return
		}
		r, err := windows.WaitForMultipleObjects([]windows.Handle{ev, w.stop}, false, windows.INFINITE)
		var n uint32
		if err != nil || r != windows.WAIT_OBJECT_0 {
			windows.CancelIoEx(h, ov)
			windows.GetOverlappedResult(h, ov, &n, true)
			return
		}
		if err := windows.GetOverlappedResult(h, ov, &n, false); err != nil {
			return
		}
		if n == 0 {
			// Buffer penuh sebelum sempat dibaca: perubahan terlewat.
			w.mu.Lock()
			w.lost = true
			w.mu.Unlock()
			continue
		}
		w.parse(root, buf[:n])
	}
}

func (w *Watcher) parse(root string, buf []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for off := 0; off+12 <= len(buf); {
		info := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
		name := unsafe.Slice(&info.FileName, info.FileNameLength/2)
		w.add(filepath.Join(root, windows.UTF16ToString(name)))
		if info.NextEntryOffset == 0 {
			break
		}
		off += int(info.NextEntryOffset)
	}
}

// add menampung path; w.mu harus sudah dikunci.
func (w *Watcher) add(path string) {
	if _, ok := w.pending[path]; ok {
		return
	}
	if len(w.pending) >= maxPending {
		w.lost = true
		return
	}
	w.pending[path] = struct{}{}
}

// flush mengirim path yang terkumpul paling cepat setiap Interval. Jika
// penerima belum siap, path dikembalikan dan ikut batch berikutnya.
func (w *Watcher) flush() {
	defer close(w.out)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
		}
		w.mu.Lock()
		if len(w.pending) == 0 && !w.lost {
			w.mu.Unlock()
			continue
		}
		taken, lost := w.pending, w.lost
		w.pending, w.lost = make(map[string]struct{}, len(taken)), false
		w.mu.Unlock()

		b := Batch{Paths: make([]string, 0, len(taken)), Lost: lost}
		for p := range taken {
			b.Paths = append(b.Paths, p)
		}
		select {
		case w.out <- b:
			continue
		case <-w.done:
			return
		default:
		}
		w.mu.Lock()
		for p := range taken {
			w.add(p)
		}
		w.lost = w.lost || lost
		w.mu.Unlock()
	}
}
