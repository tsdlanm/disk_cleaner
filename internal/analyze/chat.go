package analyze

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/tsdlanm/cli_app/internal/sizer"
)

// Message adalah satu pesan percakapan, berformat chat completions.
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall adalah permintaan AI untuk menjalankan sebuah alat.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // selalu "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction berisi nama alat dan argumennya (JSON).
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef menjelaskan sebuah alat kepada AI.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema
}

// Usage adalah pemakaian token yang dilaporkan layanan AI.
type Usage struct {
	In, Out  int
	Requests int
}

func (u *Usage) add(o Usage) {
	u.In += o.In
	u.Out += o.Out
	u.Requests += o.Requests
}

// Total adalah jumlah token masuk dan keluar.
func (u Usage) Total() int { return u.In + u.Out }

// Chatter adalah layanan AI yang bisa melanjutkan percakapan dan memanggil
// alat. OpenAI memenuhinya; Provider yang hanya bisa Complete tidak.
type Chatter interface {
	Chat(ctx context.Context, msgs []Message, tools []ToolDef) (Message, Usage, error)
}

// Step adalah satu langkah alat yang dijalankan saat AI menjawab.
type Step struct {
	N       int    // nomor langkah di jawaban ini; sama untuk mulai dan selesai
	Tool    string // nama alat
	Label   string // kalimat untuk pengguna, misalnya "Melihat isi pkg\mod"
	Summary string // hasil singkat; kosong sampai Done
	Done    bool
}

// Registry memberi nomor [id] pada folder/file yang bisa dirujuk AI: semua
// item di ringkasan, ditambah item yang ditemukan alat selama percakapan.
type Registry struct {
	mu     sync.Mutex
	items  map[int]Item
	byPath map[string]int
	next   int
}

// NewRegistry membuat Registry berisi item snapshot (boleh nil).
func NewRegistry(s *Snapshot) *Registry {
	r := &Registry{items: make(map[int]Item), byPath: make(map[string]int), next: 1}
	if s == nil {
		return r
	}
	for id, it := range s.Items {
		r.items[id] = it
		r.byPath[strings.ToLower(it.Path)] = id
		r.next = max(r.next, id+1)
	}
	return r
}

// Add mengembalikan id untuk path, membuat nomor baru jika belum ada.
func (r *Registry) Add(path string, isDir bool, size int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToLower(path)
	if id, ok := r.byPath[key]; ok {
		it := r.items[id]
		it.Size = size
		r.items[id] = it
		return id
	}
	id := r.next
	r.next++
	r.items[id] = Item{ID: id, Path: path, IsDir: isDir, Size: size}
	r.byPath[key] = id
	return id
}

// Get mengembalikan item dengan nomor id.
func (r *Registry) Get(id int) (Item, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[id]
	return it, ok
}

// chatMentionRe mengenali [[12]] dan [[12|hapus]].
var chatMentionRe = regexp.MustCompile(`\[\[\s*(\d+)\s*(?:\|\s*([^\]|]*?)\s*)?\]\]`)

// ParseChat membaca jawaban chat. Penanda [[id]] atau [[id|aksi]] yang
// merujuk item dikenal dicatat di Mentions dan ditulis ulang sebagai [[id]];
// penanda dengan id tidak dikenal dibuang. Aksi yang tidak ditulis atau tidak
// dikenal dibiarkan kosong (ditampilkan netral), tidak ditebak.
func ParseChat(text string, reg *Registry) *Result {
	res := &Result{Mentions: make(map[int]Mention)}
	res.Text = strings.TrimSpace(chatMentionRe.ReplaceAllStringFunc(tidyAnswer(text), func(tok string) string {
		sub := chatMentionRe.FindStringSubmatch(tok)
		id, _ := strconv.Atoi(sub[1])
		item, ok := reg.Get(id)
		if !ok {
			return ""
		}
		act := Action(strings.ToLower(sub[2]))
		if act != ActionDelete && act != ActionReview && act != ActionKeep {
			act = ""
		}
		if prev, seen := res.Mentions[id]; !seen || prev.Action == "" {
			res.Mentions[id] = Mention{Item: item, Action: act}
		}
		return fmt.Sprintf("[[%d]]", id)
	}))
	return res
}

// maxSteps membatasi jumlah putaran alat untuk satu pertanyaan.
const maxSteps = 6

// Session adalah percakapan tentang satu folder yang sudah dianalisis. Awalnya
// berisi ringkasan folder dan jawaban analisis pertama, supaya pertanyaan
// lanjutan bisa merujuk ke keduanya.
type Session struct {
	Root  string
	Reg   *Registry
	Tools *Toolbox
	Usage Usage

	msgs []Message
}

// NewSession memulai percakapan dari snapshot dan jawaban analisis pertama
// (boleh nil). measure menghitung ukuran folder untuk alat; nil memakai
// penghitung bawaan.
func NewSession(s *Snapshot, first *Result, measure func(string) sizer.Info) *Session {
	reg := NewRegistry(s)
	sess := &Session{Root: s.Root, Reg: reg, Tools: NewToolbox(s.Root, reg, measure)}
	sess.msgs = []Message{
		{Role: "system", Content: ChatPrompt},
		{Role: "user", Content: UserPrompt(s)},
	}
	if first != nil {
		sess.msgs = append(sess.msgs, Message{Role: "assistant", Content: first.Text})
	}
	return sess
}

// Questions adalah jumlah pertanyaan lanjutan yang sudah dijawab.
func (s *Session) Questions() int {
	n := 0
	for i, m := range s.msgs {
		if m.Role == "user" && i >= 2 {
			n++
		}
	}
	return n
}

// Reset mengembalikan percakapan ke keadaan awal: ringkasan dan jawaban pertama.
func (s *Session) Reset() {
	keep := 2
	if len(s.msgs) > 2 && s.msgs[2].Role == "assistant" {
		keep = 3
	}
	s.msgs = s.msgs[:min(keep, len(s.msgs))]
}

// Ask mengirim pertanyaan dan menjalankan alat yang diminta AI sampai ada
// jawaban akhir. onStep dipanggil saat alat mulai dan selesai (boleh nil).
// Jika gagal, percakapan kembali ke keadaan sebelum pertanyaan.
func (s *Session) Ask(ctx context.Context, c Chatter, question string, onStep func(Step)) (*Result, error) {
	start := len(s.msgs)
	s.msgs = append(s.msgs, Message{Role: "user", Content: question})
	fail := func(err error) (*Result, error) {
		s.msgs = s.msgs[:start]
		return nil, err
	}

	nudged := false
	step := 0
	for turn := 0; ; turn++ {
		tools := s.Tools.Defs()
		if turn >= maxSteps {
			// Cukup mencari: minta jawaban dari data yang sudah ada.
			tools = nil
			if !nudged {
				s.msgs = append(s.msgs, Message{Role: "user", Content: "Jawab sekarang dari data yang sudah ada, tanpa memanggil alat lagi."})
				nudged = true
			}
		}
		msg, u, err := c.Chat(ctx, s.msgs, tools)
		s.Usage.add(u)
		if err != nil {
			return fail(err)
		}
		if len(msg.ToolCalls) == 0 {
			if strings.TrimSpace(msg.Content) == "" {
				return fail(errors.New("AI tidak mengembalikan jawaban"))
			}
			s.msgs = append(s.msgs, msg)
			return ParseChat(msg.Content, s.Reg), nil
		}
		s.msgs = append(s.msgs, msg)
		for _, call := range msg.ToolCalls {
			step++
			label := s.Tools.Describe(call.Function.Name, call.Function.Arguments)
			if onStep != nil {
				onStep(Step{N: step, Tool: call.Function.Name, Label: label})
			}
			out, summary := s.Tools.Run(ctx, call.Function.Name, call.Function.Arguments)
			if onStep != nil {
				onStep(Step{N: step, Tool: call.Function.Name, Label: label, Summary: summary, Done: true})
			}
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			s.msgs = append(s.msgs, Message{Role: "tool", ToolCallID: call.ID, Content: out})
		}
	}
}
