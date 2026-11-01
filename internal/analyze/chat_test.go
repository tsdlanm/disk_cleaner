package analyze

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeSized(t *testing.T, path string, size int, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseChat(t *testing.T) {
	reg := NewRegistry(&Snapshot{Items: map[int]Item{
		1: {ID: 1, Path: `C:\x`, IsDir: true},
		2: {ID: 2, Path: `C:\x\a.iso`, Size: 5},
	}})
	res := ParseChat("Hapus [[2|hapus]], cek [[ 1 | CEK ]], buang [[99]] dan sebut lagi [[2]]. Aksi aneh [[1|bebas]].", reg)

	if want := "Hapus [[2]], cek [[1]], buang  dan sebut lagi [[2]]. Aksi aneh [[1]]."; res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	if len(res.Mentions) != 2 || res.Mentions[2].Action != ActionDelete || res.Mentions[1].Action != ActionReview {
		t.Errorf("Mentions = %+v", res.Mentions)
	}
	if res.Mentions[2].Path != `C:\x\a.iso` {
		t.Errorf("item tidak terisi: %+v", res.Mentions[2])
	}
	// Segments memecah teks di setiap penanda yang tersisa.
	var ids []int
	for _, sg := range res.Segments() {
		if sg.ID > 0 {
			ids = append(ids, sg.ID)
		}
	}
	if !slices.Equal(ids, []int{2, 1, 2, 1}) {
		t.Errorf("ids = %v", ids)
	}

	// Tanpa aksi: netral, tidak ditebak.
	if r := ParseChat("lihat [[2]]", reg); r.Mentions[2].Action != "" {
		t.Errorf("aksi tanpa penanda = %q, want kosong", r.Mentions[2].Action)
	}
}

// scripted membalas dengan jawaban yang sudah disiapkan dan mencatat permintaan.
type scripted struct {
	replies []Message
	err     error // dikembalikan jika replies habis
	calls   [][]Message
	tools   [][]ToolDef
}

func (s *scripted) Chat(_ context.Context, msgs []Message, tools []ToolDef) (Message, Usage, error) {
	s.calls = append(s.calls, slices.Clone(msgs))
	s.tools = append(s.tools, tools)
	if len(s.replies) == 0 {
		return Message{}, Usage{}, s.err
	}
	r := s.replies[0]
	s.replies = s.replies[1:]
	return r, Usage{In: 100, Out: 10, Requests: 1}, nil
}

func toolCall(id, name, args string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: id, Type: "function", Function: ToolFunction{Name: name, Arguments: args}}}}
}

func text(s string) Message { return Message{Role: "assistant", Content: s} }

func newSession(t *testing.T) (*Session, string) {
	t.Helper()
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "big", "a.bin"), 3<<20, 0)
	writeSized(t, filepath.Join(root, "small.txt"), 100, 0)
	snap := &Snapshot{Root: root, Text: "[1] root\\\n", Items: map[int]Item{1: {ID: 1, Path: root, IsDir: true}}}
	return NewSession(snap, &Result{Text: "Ringkasan awal."}, nil), root
}

func TestSessionAskUsesTools(t *testing.T) {
	sess, root := newSession(t)
	ai := &scripted{replies: []Message{
		toolCall("c1", "list_dir", `{}`),
		text("Isinya: [[2|cek]] perlu dicek."),
	}}
	var steps []Step
	res, err := sess.Ask(t.Context(), ai, "apa isi folder ini?", func(s Step) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}

	// Alat dijalankan, dua kali dilaporkan (mulai dan selesai).
	if len(steps) != 2 || steps[0].Done || !steps[1].Done || steps[1].Label != "Melihat isi folder ini" || !strings.Contains(steps[1].Summary, "2 item") {
		t.Errorf("steps = %+v", steps)
	}
	// Item yang ditemukan alat mendapat nomor baru yang bisa dirujuk jawaban.
	big, ok := sess.Reg.Get(2)
	if !ok || big.Path != filepath.Join(root, "big") || !big.IsDir {
		t.Fatalf("id 2 = %+v, %v", big, ok)
	}
	if m := res.Mentions[2]; m.Path != big.Path || m.Action != ActionReview {
		t.Errorf("mention = %+v", m)
	}

	// Permintaan kedua memuat hasil alat; riwayat dimulai dari ringkasan dan jawaban pertama.
	second := ai.calls[1]
	if second[0].Role != "system" || !strings.Contains(second[1].Content, "[1] root") || second[2].Content != "Ringkasan awal." {
		t.Errorf("riwayat awal salah: %+v", second[:3])
	}
	last := second[len(second)-1]
	if last.Role != "tool" || last.ToolCallID != "c1" || !strings.Contains(last.Content, "big\\") {
		t.Errorf("hasil alat = %+v", last)
	}
	if len(ai.tools[0]) != 2 {
		t.Errorf("alat yang ditawarkan = %d, want 2", len(ai.tools[0]))
	}
	if sess.Usage.Requests != 2 || sess.Usage.Total() != 220 || sess.Questions() != 1 {
		t.Errorf("usage %+v, pertanyaan %d", sess.Usage, sess.Questions())
	}
}

func TestSessionAskErrorRollsBack(t *testing.T) {
	sess, _ := newSession(t)
	before := len(sess.msgs)
	ai := &scripted{err: errors.New("kuota habis")}
	if res, err := sess.Ask(t.Context(), ai, "tanya", nil); err == nil || res != nil {
		t.Fatalf("res %v, err %v; want error", res, err)
	}
	if len(sess.msgs) != before || sess.Questions() != 0 {
		t.Errorf("riwayat tidak dikembalikan: %d pesan (awal %d)", len(sess.msgs), before)
	}

	// Jawaban kosong juga error.
	ai = &scripted{replies: []Message{text("  ")}}
	if _, err := sess.Ask(t.Context(), ai, "tanya", nil); err == nil || len(sess.msgs) != before {
		t.Errorf("jawaban kosong: err %v, %d pesan", err, len(sess.msgs))
	}
}

func TestSessionAskStopsLoopingTools(t *testing.T) {
	sess, _ := newSession(t)
	var replies []Message
	for range maxSteps {
		replies = append(replies, toolCall("c", "list_dir", `{}`))
	}
	replies = append(replies, text("Cukup, ini jawabannya."))
	ai := &scripted{replies: replies}
	res, err := sess.Ask(t.Context(), ai, "cari terus", nil)
	if err != nil || res.Text != "Cukup, ini jawabannya." {
		t.Fatalf("res %+v, err %v", res, err)
	}
	// Setelah batas, alat tidak ditawarkan lagi dan AI diminta menjawab.
	if len(ai.calls) != maxSteps+1 || ai.tools[maxSteps] != nil {
		t.Errorf("%d panggilan, alat terakhir %v", len(ai.calls), ai.tools[maxSteps])
	}
	var nudged bool
	for _, m := range ai.calls[maxSteps] {
		nudged = nudged || strings.Contains(m.Content, "Jawab sekarang")
	}
	if !nudged {
		t.Error("AI tidak diminta menjawab setelah batas alat")
	}
}

func TestSessionReset(t *testing.T) {
	sess, _ := newSession(t)
	ai := &scripted{replies: []Message{text("jawab 1"), text("jawab 2")}}
	sess.Ask(t.Context(), ai, "satu", nil)
	sess.Ask(t.Context(), ai, "dua", nil)
	if sess.Questions() != 2 {
		t.Fatalf("pertanyaan = %d", sess.Questions())
	}
	sess.Reset()
	if sess.Questions() != 0 || len(sess.msgs) != 3 || sess.msgs[2].Content != "Ringkasan awal." {
		t.Errorf("setelah Reset: %d pesan, %d pertanyaan", len(sess.msgs), sess.Questions())
	}
}

func TestToolboxStaysInsideRoot(t *testing.T) {
	sess, root := newSession(t)
	outside := t.TempDir()
	writeSized(t, filepath.Join(outside, "rahasia.txt"), 10, 0)

	for _, args := range []string{
		`{"path":"..\\..\\"}`,
		`{"path":` + jsonString(outside) + `}`,
		`{"path":` + jsonString(root+`-lain`) + `}`,
	} {
		for _, tool := range []string{"list_dir", "find_files"} {
			out, sum := sess.Tools.Run(t.Context(), tool, args)
			if !strings.HasPrefix(out, "Gagal:") || !strings.Contains(out, "di luar folder") || !strings.HasPrefix(sum, "gagal") {
				t.Errorf("%s %s: out %q, ringkasan %q", tool, args, out, sum)
			}
		}
	}
	// id yang tidak dikenal dan alat yang tidak ada.
	if out, _ := sess.Tools.Run(t.Context(), "list_dir", `{"id":999}`); !strings.Contains(out, "id 999 tidak dikenal") {
		t.Errorf("id tidak dikenal: %q", out)
	}
	if out, _ := sess.Tools.Run(t.Context(), "hapus_semua", `{}`); !strings.Contains(out, "tidak dikenal") {
		t.Errorf("alat tidak ada: %q", out)
	}
	if out, _ := sess.Tools.Run(t.Context(), "list_dir", `bukan json`); !strings.Contains(out, "bukan JSON") {
		t.Errorf("argumen rusak: %q", out)
	}
	// Path relatif dan id di dalam root diterima.
	sess.Reg.Add(filepath.Join(root, "big"), true, 3<<20)
	for _, args := range []string{`{"path":"big"}`, `{"id":2}`} {
		if out, _ := sess.Tools.Run(t.Context(), "list_dir", args); !strings.Contains(out, "a.bin") {
			t.Errorf("list_dir %s: %q", args, out)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestFindFiles(t *testing.T) {
	root := t.TempDir()
	writeSized(t, filepath.Join(root, "lama", "tua.iso"), 1<<20, 400*24*time.Hour)
	writeSized(t, filepath.Join(root, "baru", "baru.bin"), 1<<20, 0)
	writeSized(t, filepath.Join(root, "kecil.txt"), 100, 400*24*time.Hour)
	tb := NewToolbox(root, NewRegistry(nil), nil)

	run := func(args string) (string, string) {
		t.Helper()
		return tb.Run(t.Context(), "find_files", args)
	}
	out, sum := run(`{"min_size_mb":0.5,"older_than_days":365}`)
	if !strings.Contains(out, "tua.iso") || strings.Contains(out, "baru.bin") || strings.Contains(out, "kecil.txt") || sum != "1 file · 1.0 MB" {
		t.Errorf("lama dan besar: %q (%s)", out, sum)
	}
	if out, _ := run(`{"name_contains":"BARU"}`); !strings.Contains(out, "baru.bin") || strings.Contains(out, "tua.iso") {
		t.Errorf("nama memuat: %q", out)
	}
	// Tanpa kriteria hanya file besar (≥ 50 MB), jadi tidak ada hasil di sini.
	if _, sum := run(`{}`); sum != "0 file · 0 B" {
		t.Errorf("tanpa kriteria: %q", sum)
	}
	// Hasil bernomor, dan nomornya bisa dipakai lagi.
	out, _ = run(`{"min_size_mb":0.5}`)
	if !strings.Contains(out, "[1] ") || !strings.Contains(out, "[2] ") {
		t.Errorf("hasil tidak bernomor: %q", out)
	}
	if got := tb.Describe("find_files", `{"min_size_mb":100,"older_than_days":30,"path":"lama"}`); got != "Mencari file di lama (≥ 100 MB, tidak diubah ≥ 30 hari)" {
		t.Errorf("Describe = %q", got)
	}
}

func TestToolResultIsTruncated(t *testing.T) {
	root := t.TempDir()
	for i := range 40 {
		writeSized(t, filepath.Join(root, strings.Repeat("n", 200)+fmt.Sprintf("%02d.bin", i)), 10, 0)
	}
	tb := NewToolbox(root, NewRegistry(nil), nil)
	out, _ := tb.Run(t.Context(), "list_dir", `{}`)
	if !strings.HasSuffix(out, "… hasil dipotong") || len(out) > maxToolChars+40 {
		t.Errorf("hasil alat %d byte, ujung %q; harus dipotong di %d", len(out), out[max(len(out)-30, 0):], maxToolChars)
	}
}
func chatServer(t *testing.T, handler func(req map[string]any, w http.ResponseWriter)) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		handler(req, w)
	}))
	t.Cleanup(srv.Close)
	return NewOpenAI(srv.URL, "kunci", "model-utama", "model-cadangan")
}

func TestOpenAIChat(t *testing.T) {
	var got map[string]any
	o := chatServer(t, func(req map[string]any, w http.ResponseWriter) {
		got = req
		w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"content":null,"tool_calls":[
			{"function":{"name":"list_dir","arguments":"{\"id\":3}"}}]}}],
			"usage":{"prompt_tokens":1200,"completion_tokens":34}}`))
	})
	msgs := []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "u"}}
	msg, u, err := o.Chat(t.Context(), msgs, NewToolbox(`C:\x`, NewRegistry(nil), nil).Defs())
	if err != nil {
		t.Fatal(err)
	}

	// Permintaan: pesan dan alat format OpenAI, tanpa response_format.
	if got["model"] != "model-utama" || len(got["messages"].([]any)) != 2 || len(got["tools"].([]any)) != 2 || got["response_format"] != nil {
		t.Errorf("permintaan = %v", got)
	}
	fn := got["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "list_dir" || fn["parameters"] == nil {
		t.Errorf("definisi alat = %v", fn)
	}
	// Jawaban: id panggilan yang kosong diisi, token terbaca.
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Type != "function" || msg.ToolCalls[0].Function.Arguments != `{"id":3}` {
		t.Errorf("tool_calls = %+v", msg.ToolCalls)
	}
	if u != (Usage{In: 1200, Out: 34, Requests: 1}) {
		t.Errorf("usage = %+v", u)
	}
}

func TestOpenAIChatErrors(t *testing.T) {
	// Model utama sibuk: model cadangan dipakai.
	var models []string
	o := chatServer(t, func(req map[string]any, w http.ResponseWriter) {
		models = append(models, req["model"].(string))
		if req["model"] == "model-utama" {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"overloaded"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"halo"}}]}`))
	})
	msg, _, err := o.Chat(t.Context(), []Message{{Role: "user", Content: "u"}}, nil)
	if err != nil || msg.Content != "halo" || !slices.Equal(models, []string{"model-utama", "model-cadangan"}) {
		t.Errorf("cadangan: %+v, %v, %v", msg, err, models)
	}

	for name, body := range map[string]string{
		"terpotong": `{"choices":[{"finish_reason":"length","message":{"content":"set"}}]}`,
		"kosong":    `{"choices":[{"finish_reason":"stop","message":{"content":""}}]}`,
		"tanpa":     `{"choices":[]}`,
	} {
		o := chatServer(t, func(_ map[string]any, w http.ResponseWriter) { w.Write([]byte(body)) })
		if _, _, err := o.Chat(t.Context(), []Message{{Role: "user", Content: "u"}}, nil); err == nil {
			t.Errorf("%s: seharusnya error", name)
		}
	}
	o = chatServer(t, func(_ map[string]any, w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	if _, _, err := o.Chat(t.Context(), []Message{{Role: "user", Content: "u"}}, nil); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("key ditolak: %v", err)
	}
}
