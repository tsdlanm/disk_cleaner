// Package sizer menghitung ukuran folder di background dan menyimpan
// hasilnya di cache, termasuk ukuran setiap folder turunannya.
package sizer

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// State adalah status ukuran sebuah folder.
type State int

const (
	Unknown   State = iota // belum dijadwalkan
	Computing              // sedang/akan dihitung; Size berisi angka sementara
	Done
)

// Info adalah hasil hitung ukuran sebuah folder.
type Info struct {
	Size   int64
	Files  int64
	Dirs   int64
	Denied bool  // folder ini sendiri tidak bisa dibaca
	Locked int64 // jumlah folder turunan yang tidak bisa dibaca
}

// Sizer menjalankan worker yang menghitung ukuran folder dari antrean LIFO:
// permintaan terbaru (folder yang sedang dibuka) selalu didahulukan.
type Sizer struct {
	mu    sync.RWMutex
	cache map[string]Info
	live  map[string]*atomic.Int64 // byte sementara untuk job yang sedang jalan
	gen   uint64                   // naik saat Invalidate; hasil lama dibuang

	qmu    sync.Mutex
	cond   *sync.Cond
	stack  []string
	queued map[string]bool // ada di stack atau sedang dikerjakan

	sem chan struct{} // batas goroutine walker
}

// New membuat Sizer dan menjalankan worker-nya.
func New() *Sizer {
	s := &Sizer{
		cache:  make(map[string]Info),
		live:   make(map[string]*atomic.Int64),
		queued: make(map[string]bool),
		sem:    make(chan struct{}, runtime.NumCPU()*4),
	}
	s.cond = sync.NewCond(&s.qmu)
	for range max(runtime.NumCPU(), 2) {
		go s.worker()
	}
	return s
}

// Get mengembalikan ukuran folder dari cache, atau ukuran sementara jika
// folder itu sedang dihitung sebagai job.
func (s *Sizer) Get(path string) (Info, State) {
	s.mu.RLock()
	inf, ok := s.cache[path]
	live := s.live[path]
	s.mu.RUnlock()
	if ok {
		return inf, Done
	}
	if live != nil {
		return Info{Size: live.Load()}, Computing
	}
	s.qmu.Lock()
	q := s.queued[path]
	s.qmu.Unlock()
	if q {
		return Info{}, Computing
	}
	return Info{}, Unknown
}

// Request menjadwalkan hitung ukuran untuk paths. Path yang sudah ada di
// cache atau sudah dijadwalkan dilewati. Path pertama diproses lebih dulu.
func (s *Sizer) Request(paths ...string) {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	added := false
	for i := len(paths) - 1; i >= 0; i-- {
		p := paths[i]
		if s.queued[p] || s.cached(p) {
			continue
		}
		s.queued[p] = true
		s.stack = append(s.stack, p)
		added = true
	}
	if added {
		s.cond.Broadcast()
	}
}

// Pending mengembalikan jumlah job yang menunggu atau sedang berjalan.
func (s *Sizer) Pending() int {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	return len(s.queued)
}

// Invalidate menghapus cache path, semua folder turunannya, dan semua folder
// induknya (yang ukurannya ikut berubah). Job yang sedang berjalan tidak lagi
// menyimpan hasilnya.
func (s *Sizer) Invalidate(path string) {
	path = filepath.Clean(path)
	prefix := path
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	for k := range s.cache {
		if k == path || strings.HasPrefix(k, prefix) {
			delete(s.cache, k)
		}
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		delete(s.cache, p)
		if filepath.Dir(p) == p {
			break
		}
	}
}

func (s *Sizer) cached(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.cache[path]
	return ok
}

func (s *Sizer) worker() {
	for {
		s.qmu.Lock()
		for len(s.stack) == 0 {
			s.cond.Wait()
		}
		path := s.stack[len(s.stack)-1]
		s.stack = s.stack[:len(s.stack)-1]
		s.qmu.Unlock()

		live := new(atomic.Int64)
		s.mu.Lock()
		gen := s.gen
		s.live[path] = live
		s.mu.Unlock()

		s.walk(path, gen, live)

		s.mu.Lock()
		delete(s.live, path)
		s.mu.Unlock()
		s.qmu.Lock()
		delete(s.queued, path)
		s.qmu.Unlock()
	}
}

// walk menghitung ukuran path secara rekursif dan menyimpan hasil setiap
// folder yang dilewati ke cache. Folder yang sudah ada di cache tidak
// dipindai ulang.
func (s *Sizer) walk(path string, gen uint64, live *atomic.Int64) Info {
	s.mu.RLock()
	if inf, ok := s.cache[path]; ok {
		s.mu.RUnlock()
		live.Add(inf.Size)
		return inf
	}
	s.mu.RUnlock()

	entries, err := os.ReadDir(path)
	var inf Info
	if err != nil && len(entries) == 0 {
		inf.Denied = true
		inf.Locked = 1
		s.store(path, inf, gen)
		return inf
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		files int64
		bytes int64
	)
	merge := func(c Info) {
		mu.Lock()
		inf.Size += c.Size
		inf.Files += c.Files
		inf.Dirs += c.Dirs + 1
		inf.Locked += c.Locked
		mu.Unlock()
	}
	for _, e := range entries {
		if IsRealDir(e) {
			child := filepath.Join(path, e.Name())
			select {
			case s.sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-s.sem }()
					merge(s.walk(child, gen, live))
				}()
			default:
				merge(s.walk(child, gen, live))
			}
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		// Di Windows, Info() dari ReadDir tidak memerlukan syscall tambahan.
		fi, err := e.Info()
		if err != nil {
			continue
		}
		bytes += fi.Size()
		files++
	}
	live.Add(bytes)
	wg.Wait()

	inf.Size += bytes
	inf.Files += files
	s.store(path, inf, gen)
	return inf
}

func (s *Sizer) store(path string, inf Info, gen uint64) {
	s.mu.Lock()
	if s.gen == gen {
		s.cache[path] = inf
	}
	s.mu.Unlock()
}

// IsRealDir bernilai true untuk direktori biasa, bukan symlink atau junction
// (Go melaporkan junction Windows sebagai ModeIrregular).
func IsRealDir(e os.DirEntry) bool {
	return e.IsDir() && e.Type()&(os.ModeSymlink|os.ModeIrregular) == 0
}
