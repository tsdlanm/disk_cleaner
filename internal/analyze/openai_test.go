package analyze

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI menjalankan server tiruan berformat OpenAI dan mengembalikan
// provider yang mengarah ke server itu.
func fakeAPI(t *testing.T, fallback string, handler http.HandlerFunc) *OpenAI {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewOpenAI(srv.URL+"/v1", "key-apa-saja", "model-utama", fallback)
}

func reply(content string) string {
	b, _ := json.Marshal(map[string]any{"choices": []map[string]any{
		{"message": map[string]string{"content": content}, "finish_reason": "stop"},
	}})
	return string(b)
}

func TestOpenAIComplete(t *testing.T) {
	var got map[string]any
	p := fakeAPI(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer key-apa-saja" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, reply(`{"answer":"ok","mentions":[]}`))
	})
	text, err := p.Complete(t.Context(), "sistem", "data", resultSchema)
	if err != nil || text != `{"answer":"ok","mentions":[]}` {
		t.Fatalf("text %q err %v", text, err)
	}
	msgs := got["messages"].([]any)
	if got["model"] != "model-utama" || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "data" {
		t.Errorf("request = %v", got)
	}
	if rf := got["response_format"].(map[string]any); rf["type"] != "json_schema" {
		t.Errorf("response_format = %v", rf)
	}
}

func TestOpenAIErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"error":{"message":"invalid x-api-key"}}`, ErrInvalidKey.Error()},
		// Gemini mengembalikan error berupa array dan 400 untuk key salah.
		{400, `[{"error":{"code":400,"message":"API key not valid. Please pass a valid API key."}}]`, ErrInvalidKey.Error()},
		{429, `{"error":{"message":"quota"}}`, "batas kuota"},
		{404, `{"error":{"message":"model not found"}}`, "tidak ditemukan"},
		{200, `{"choices":[{"message":{"content":"{"},"finish_reason":"length"}]}`, "terpotong"},
	}
	for _, c := range cases {
		p := fakeAPI(t, "", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
		_, err := p.Complete(t.Context(), "s", "u", resultSchema)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("status %d: err = %v, want berisi %q", c.status, err, c.want)
		}
		if c.want == ErrInvalidKey.Error() && !errors.Is(err, ErrInvalidKey) {
			t.Errorf("status %d: harus ErrInvalidKey, dapat %v", c.status, err)
		}
	}
}

func TestOpenAIBusyFallback(t *testing.T) {
	var models []string
	p := fakeAPI(t, "model-cadangan", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		models = append(models, req["model"].(string))
		if req["model"] == "model-utama" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, reply(`{"answer":"ok","mentions":[]}`))
	})
	if _, err := p.Complete(t.Context(), "s", "u", resultSchema); err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[1] != "model-cadangan" {
		t.Errorf("model = %v", models)
	}
}

func TestOpenAINoResponseFormat(t *testing.T) {
	calls := 0
	p := fakeAPI(t, "", func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["response_format"]; ok {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"response_format json_schema is not supported"}}`)
			return
		}
		io.WriteString(w, reply(`{"answer":"ok","mentions":[]}`))
	})
	if _, err := p.Complete(t.Context(), "s", "u", resultSchema); err != nil || calls != 2 {
		t.Errorf("err %v, calls %d; want diulang tanpa response_format", err, calls)
	}
}

// fakeProvider mengembalikan jawaban secara berurutan.
type fakeProvider struct {
	replies []string
	users   []string
}

func (f *fakeProvider) Complete(_ context.Context, _, user string, _ map[string]any) (string, error) {
	f.users = append(f.users, user)
	r := f.replies[0]
	f.replies = f.replies[1:]
	return r, nil
}

func TestAnalyzeTolerant(t *testing.T) {
	s := &Snapshot{Items: map[int]Item{2: {ID: 2, Path: `C:\x\cache`, IsDir: true}}}

	// JSON dibungkus blok kode dan teks lain tetap terbaca.
	p := &fakeProvider{replies: []string{"Berikut hasilnya:\n```json\n{\"answer\":\"• [[2]] cache\",\"mentions\":[{\"id\":2,\"action\":\"hapus\"}]}\n```"}}
	res, err := Analyze(t.Context(), p, s)
	if err != nil || res.Mentions[2].Action != ActionDelete {
		t.Fatalf("res %+v err %v", res, err)
	}

	// Jawaban bukan JSON: diulang sekali dengan pengingat format.
	p = &fakeProvider{replies: []string{"maaf, ini bukan json", `{"answer":"ok","mentions":[]}`}}
	if res, err := Analyze(t.Context(), p, s); err != nil || res.Text != "ok" {
		t.Fatalf("ulang: res %+v err %v", res, err)
	}
	if len(p.users) != 2 || !strings.Contains(p.users[1], "HANYA dengan satu objek JSON") {
		t.Errorf("permintaan ulang tanpa pengingat: %q", p.users)
	}

	// Tidak menyebut satu pun folder (kasus nyata gemini flash-lite): diulang
	// sekali dengan pengingat untuk menyebut semua folder.
	s.Items[1] = Item{ID: 1, Path: `C:\x`, IsDir: true}
	p = &fakeProvider{replies: []string{
		`{"answer":"Ringkasan saja.","mentions":[]}`,
		`{"answer":"Aman dihapus:\n• [[2]] cache","mentions":[{"id":2,"action":"hapus","reason":"cache"}]}`,
	}}
	res, err = Analyze(t.Context(), p, s)
	if err != nil || len(res.Mentions) != 1 || len(p.users) != 2 || !strings.Contains(p.users[1], "tidak menyebut satu pun") {
		t.Errorf("ulang tanpa sebutan: res %+v err %v permintaan %d", res, err, len(p.users))
	}
}

func TestCleanToken(t *testing.T) {
	// NUL di depan (kasus nyata dari paste), baris baru, spasi, dan karakter
	// tak terlihat dibuang; isi key tetap utuh.
	in := "\x00 AQ.Ab8-key_123\r\n\t" + string(rune(0xFEFF)) + string(rune(0x200B))
	if got := CleanToken(in); got != "AQ.Ab8-key_123" {
		t.Errorf("CleanToken = %q", got)
	}
}

func TestConfig(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	c := DefaultConfig
	if c.Provider != "gemini" || !c.NeedsKey() {
		t.Fatalf("bawaan: provider %q, butuh key %v", c.Provider, c.NeedsKey())
	}

	// Key apa pun dipakai apa adanya; preset mengisi Base URL dan cadangan.
	c.APIKey = "key-bentuk-apa-saja"
	p, err := c.NewProvider()
	if err != nil {
		t.Fatal(err)
	}
	if o := p.(*OpenAI); o.key != "key-bentuk-apa-saja" || o.model != DefaultGeminiModel ||
		!strings.Contains(o.baseURL, "generativelanguage") || o.fallback == "" {
		t.Errorf("gemini: %+v", o)
	}

	// Claude: model ikut ke bawaan Claude, key tetap.
	c.SetProvider("claude")
	if o, _ := c.NewProvider(); o.(*OpenAI).model != "claude-opus-5-5" || !strings.Contains(o.(*OpenAI).baseURL, "anthropic") {
		t.Errorf("claude: %+v", o)
	}

	// Lainnya: Base URL dan model diisi sendiri.
	c.SetProvider("custom")
	c.Model = ""
	if _, err := c.NewProvider(); err == nil {
		t.Error("custom tanpa Base URL seharusnya error")
	}
	c.BaseURL, c.Model = "https://openrouter.ai/api/v1", "meta/llama-gratis"
	if o, err := c.NewProvider(); err != nil || o.(*OpenAI).baseURL != "https://openrouter.ai/api/v1/" {
		t.Errorf("custom: %+v, %v", o, err)
	}

	// Tersimpan dan terbaca lagi.
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig()
	if err != nil || got.Provider != "custom" || got.BaseURL != c.BaseURL || got.Model != c.Model || got.APIKey != c.APIKey {
		t.Errorf("LoadConfig = %+v, %v", got, err)
	}
}
