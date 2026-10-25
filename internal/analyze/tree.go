// Package analyze menyiapkan ringkasan isi folder untuk dianalisis AI dan
// membaca saran yang dikembalikan AI.
package analyze

import (
	"container/heap"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/sizer"
)

// Limits mengatur seberapa besar ringkasan yang dikirim ke AI.
type Limits struct {
	MaxLines int   // jumlah baris maksimal ringkasan
	MinSize  int64 // folder/file di bawah ukuran ini tidak dirinci sendiri
}

// DefaultLimits dipakai jika konfigurasi tidak mengatur batas.
var DefaultLimits = Limits{MaxLines: 1500, MinSize: 1 << 20}

// collapsed berisi nama folder yang isinya sudah jelas polanya, sehingga
// cukup dikirim sebagai satu baris berisi totalnya.
var collapsed = map[string]bool{
	"node_modules": true, ".git": true, "__pycache__": true, ".venv": true, "venv": true,
	".gradle": true, ".m2": true, ".nuget": true, ".npm": true, ".cache": true,
	"cache": true, "caches": true, "temp": true, "tmp": true, "$recycle.bin": true,
	"winsxs": true, "system volume information": true,
}

// Item adalah satu baris bernomor di ringkasan yang bisa dirujuk AI.
type Item struct {
	ID    int
	Path  string
	IsDir bool
	Size  int64
}

// Snapshot adalah ringkasan folder yang siap dikirim ke AI.
type Snapshot struct {
	Root    string
	Size    int64
	Files   int64
	Dirs    int64
	Text    string       // pohon bertingkat dengan nomor [id] per baris
	Lines   int          // jumlah baris di Text
	LineIDs []int        // id item di setiap baris Text; 0 untuk baris gabungan
	Items   map[int]Item // id -> item
}

// Progress berisi penghitung yang aman dibaca selama Build berjalan.
type Progress struct {
	Files atomic.Int64
	Dirs  atomic.Int64
	Bytes atomic.Int64
}

type node struct {
	name      string
	path      string
	dir       bool
	size      int64
	files     int64 // jumlah file di dalamnya (rekursif)
	dirs      int64 // jumlah folder di dalamnya (rekursif)
	mod       time.Time
	children  []*node // subfolder dan file besar
	smallN    int64   // file kecil langsung di folder ini
	smallSize int64
	collapsed bool
	denied    bool
	expanded  bool
}

// Build memindai root dan menyusun ringkasan dalam batas lim. Folder
// terbesar dirinci lebih dulu sampai jatah baris habis.
func Build(ctx context.Context, root string, lim Limits, p *Progress) (*Snapshot, error) {
	if p == nil {
		p = &Progress{}
	}
	if lim.MaxLines <= 0 {
		lim.MaxLines = DefaultLimits.MaxLines
	}
	if lim.MinSize <= 0 {
		lim.MinSize = DefaultLimits.MinSize
	}
	w := &walker{ctx: ctx, p: p, lim: lim}
	n := w.walk(root, root, true)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.denied {
		return nil, fmt.Errorf("tidak bisa membaca %s: akses ditolak", root)
	}
	expand(n, lim)
	return render(n, lim), nil
}

type walker struct {
	ctx context.Context
	p   *Progress
	lim Limits
}

// walk membangun node untuk path. Jika keep false, hanya ukuran dan jumlah
// yang dihitung (dipakai di dalam folder yang tidak dirinci).
func (w *walker) walk(path, name string, keep bool) *node {
	n := &node{name: name, path: path, dir: true}
	if w.ctx.Err() != nil {
		return n
	}
	n.collapsed = collapsed[strings.ToLower(name)] && path != name
	keepChildren := keep && !n.collapsed

	entries, err := os.ReadDir(path)
	if err != nil && len(entries) == 0 {
		n.denied = true
		return n
	}
	w.p.Dirs.Add(1)

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	add := func(c *node) {
		mu.Lock()
		n.size += c.size
		n.files += c.files
		n.dirs += c.dirs + 1
		if keepChildren {
			n.children = append(n.children, c)
		}
		mu.Unlock()
	}
	var files, bytes int64
	for _, e := range entries {
		if sizer.IsRealDir(e) {
			child := filepath.Join(path, e.Name())
			select {
			case sizer.Slots <- struct{}{}:
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { <-sizer.Slots }()
					add(w.walk(child, e.Name(), keepChildren))
				}()
			default:
				add(w.walk(child, e.Name(), keepChildren))
			}
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files++
		bytes += fi.Size()
		if keepChildren && fi.Size() >= w.lim.MinSize {
			mu.Lock()
			n.children = append(n.children, &node{
				name: e.Name(), path: filepath.Join(path, e.Name()),
				size: fi.Size(), mod: fi.ModTime(),
			})
			mu.Unlock()
		} else {
			n.smallN++
			n.smallSize += fi.Size()
		}
	}
	w.p.Files.Add(files)
	w.p.Bytes.Add(bytes)
	wg.Wait()

	mu.Lock()
	n.size += bytes
	n.files += files
	mu.Unlock()
	return n
}

// shown memisahkan anak yang ditampilkan sendiri dari yang digabung
// menjadi satu baris "item lain".
func shown(n *node, lim Limits) (list []*node, restN, restSize int64) {
	restN, restSize = n.smallN, n.smallSize
	for _, c := range n.children {
		if c.size >= lim.MinSize {
			list = append(list, c)
		} else {
			restN += 1 + c.files
			restSize += c.size
		}
	}
	slices.SortFunc(list, func(a, b *node) int {
		if a.size != b.size {
			if a.size > b.size {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	return list, restN, restSize
}

// expandCost adalah jumlah baris yang bertambah jika n dirinci.
func expandCost(n *node, lim Limits) int {
	list, restN, _ := shown(n, lim)
	cost := len(list)
	if restN > 0 {
		cost++
	}
	return cost
}

func expandable(n *node, lim Limits) bool {
	return n.dir && !n.collapsed && !n.denied && n.size >= lim.MinSize && len(n.children) > 0
}

// expand merinci folder dari yang terbesar sampai jatah baris habis.
func expand(root *node, lim Limits) {
	root.expanded = true
	lines := 1 + expandCost(root, lim)
	h := &nodeHeap{}
	push := func(n *node) {
		list, _, _ := shown(n, lim)
		for _, c := range list {
			if expandable(c, lim) {
				heap.Push(h, c)
			}
		}
	}
	push(root)
	for h.Len() > 0 {
		n := heap.Pop(h).(*node)
		cost := expandCost(n, lim)
		if lines+cost > lim.MaxLines {
			continue // terlalu besar; folder lebih kecil mungkin masih muat
		}
		n.expanded = true
		lines += cost
		push(n)
	}
}

type nodeHeap []*node

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].size > h[j].size }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)        { *h = append(*h, x.(*node)) }
func (h *nodeHeap) Pop() any {
	old := *h
	n := old[len(old)-1]
	*h = old[:len(old)-1]
	return n
}

// render menulis pohon bertingkat. Setiap item mendapat nomor [id] yang
// dipakai AI untuk merujuknya.
func render(root *node, lim Limits) *Snapshot {
	s := &Snapshot{
		Root: root.path, Size: root.size, Files: root.files, Dirs: root.dirs,
		Items: make(map[int]Item),
	}
	var b strings.Builder
	id := 0
	var write func(n *node, depth int)
	write = func(n *node, depth int) {
		id++
		s.Items[id] = Item{ID: id, Path: n.path, IsDir: n.dir, Size: n.size}
		b.WriteString(strings.Repeat("  ", depth))
		fmt.Fprintf(&b, "[%d] %s", id, n.name)
		if n.dir {
			if !strings.HasSuffix(n.name, `\`) {
				b.WriteString(`\`)
			}
			fmt.Fprintf(&b, "  %s (%s file, %s folder", format.Bytes(n.size), format.Int(n.files), format.Int(n.dirs))
			switch {
			case n.denied:
				b.WriteString(", akses ditolak")
			case n.collapsed:
				b.WriteString(", tidak dirinci")
			}
			b.WriteString(")\n")
		} else {
			fmt.Fprintf(&b, "  %s, diubah %s\n", format.Bytes(n.size), n.mod.Format("2006-01-02"))
		}
		s.Lines++
		s.LineIDs = append(s.LineIDs, id)
		if !n.expanded {
			return
		}
		list, restN, restSize := shown(n, lim)
		for _, c := range list {
			write(c, depth+1)
		}
		if restN > 0 {
			b.WriteString(strings.Repeat("  ", depth+1))
			fmt.Fprintf(&b, "… %s item kecil lainnya, %s\n", format.Int(restN), format.Bytes(restSize))
			s.Lines++
			s.LineIDs = append(s.LineIDs, 0)
		}
	}
	write(root, 0)
	s.Text = b.String()
	return s
}
