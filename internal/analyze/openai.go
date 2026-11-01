package analyze

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ErrInvalidKey dikembalikan jika API key ditolak.
var ErrInvalidKey = errors.New("API key tidak valid")

// errBusy menandai model yang sedang kelebihan beban.
var errBusy = errors.New("layanan AI sedang sibuk; coba lagi nanti")

// errNoFormat menandai layanan yang tidak menerima response_format.
var errNoFormat = errors.New("response_format tidak didukung")

// OpenAI adalah Provider untuk semua layanan dengan API format OpenAI
// (chat completions): Gemini, Claude, OpenRouter, Groq, Ollama, dan lainnya.
type OpenAI struct {
	baseURL  string // diakhiri "/", misalnya "https://api.anthropic.com/v1/"
	key      string
	model    string
	fallback string // model cadangan jika model utama sibuk; boleh kosong
	client   *http.Client
}

// NewOpenAI membuat provider format OpenAI.
func NewOpenAI(baseURL, key, model, fallback string) *OpenAI {
	baseURL, key = CleanToken(baseURL), CleanToken(key)
	if !strings.HasSuffix(baseURL, "/") {
		baseURL += "/"
	}
	return &OpenAI{baseURL: baseURL, key: key, model: model, fallback: fallback, client: http.DefaultClient}
}

// Complete mengirim permintaan chat completions. Jika model utama sibuk,
// permintaan diulang dengan model cadangan.
func (o *OpenAI) Complete(ctx context.Context, system, user string, schema map[string]any) (string, error) {
	text, err := o.complete(ctx, o.model, system, user, schema)
	if errors.Is(err, errBusy) && o.fallback != "" && o.fallback != o.model {
		text, err = o.complete(ctx, o.fallback, system, user, schema)
	}
	return text, err
}

func (o *OpenAI) complete(ctx context.Context, model, system, user string, schema map[string]any) (string, error) {
	text, err := o.send(ctx, model, system, user, schema)
	if errors.Is(err, errNoFormat) {
		// Sebagian layanan menolak response_format; format jawaban tetap
		// diminta lewat prompt dan dibaca oleh Parse.
		text, err = o.send(ctx, model, system, user, nil)
	}
	return text, err
}

func (o *OpenAI) send(ctx context.Context, model, system, user string, schema map[string]any) (string, error) {
	req := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens": 16000,
	}
	if schema != nil {
		req["response_format"] = map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "analisis", "schema": schema, "strict": true},
		}
	}
	status, data, err := o.do(ctx, req)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", apiError(status, data, schema != nil)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("jawaban layanan AI tidak terbaca: %w", err)
	}
	if len(out.Choices) == 0 {
		return "", errors.New("layanan AI tidak mengembalikan jawaban")
	}
	c := out.Choices[0]
	switch c.FinishReason {
	case "length":
		return "", errors.New("jawaban AI terpotong karena terlalu panjang")
	case "content_filter":
		return "", errors.New("AI menolak menganalisis folder ini")
	}
	if strings.TrimSpace(c.Message.Content) == "" {
		return "", errors.New("layanan AI tidak mengembalikan jawaban")
	}
	return c.Message.Content, nil
}

// do mengirim permintaan chat completions dan mengembalikan status serta
// isi jawaban.
func (o *OpenAI) do(ctx context.Context, req map[string]any) (int, []byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.key)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0, nil, errors.New("analisis dibatalkan")
		}
		return 0, nil, fmt.Errorf("gagal menghubungi layanan AI: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

// Chat melanjutkan percakapan, dengan alat bila tools tidak kosong. Jika model
// utama sibuk, permintaan diulang dengan model cadangan.
func (o *OpenAI) Chat(ctx context.Context, msgs []Message, tools []ToolDef) (Message, Usage, error) {
	msg, u, err := o.chat(ctx, o.model, msgs, tools)
	if errors.Is(err, errBusy) && o.fallback != "" && o.fallback != o.model {
		msg, u, err = o.chat(ctx, o.fallback, msgs, tools)
	}
	return msg, u, err
}

func (o *OpenAI) chat(ctx context.Context, model string, msgs []Message, tools []ToolDef) (Message, Usage, error) {
	req := map[string]any{"model": model, "messages": msgs, "max_tokens": 8192}
	if len(tools) > 0 {
		defs := make([]map[string]any, len(tools))
		for i, t := range tools {
			defs[i] = map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}}
		}
		req["tools"] = defs
	}
	status, data, err := o.do(ctx, req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	if status != http.StatusOK {
		return Message{}, Usage{}, apiError(status, data, false)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return Message{}, Usage{}, fmt.Errorf("jawaban layanan AI tidak terbaca: %w", err)
	}
	if len(out.Choices) == 0 {
		return Message{}, Usage{}, errors.New("layanan AI tidak mengembalikan jawaban")
	}
	c := out.Choices[0]
	switch c.FinishReason {
	case "length":
		return Message{}, Usage{}, errors.New("jawaban AI terpotong karena terlalu panjang")
	case "content_filter":
		return Message{}, Usage{}, errors.New("AI menolak menjawab pertanyaan ini")
	}
	u := Usage{In: out.Usage.Prompt, Out: out.Usage.Completion, Requests: 1}
	msg := Message{Role: "assistant", Content: c.Message.Content, ToolCalls: c.Message.ToolCalls}
	for i := range msg.ToolCalls {
		// Sebagian layanan tidak memberi id panggilan; pesan hasil alat butuh id yang sama.
		if msg.ToolCalls[i].ID == "" {
			msg.ToolCalls[i].ID = fmt.Sprintf("call_%d", i+1)
		}
		msg.ToolCalls[i].Type = "function"
	}
	if strings.TrimSpace(msg.Content) == "" && len(msg.ToolCalls) == 0 {
		return Message{}, u, errors.New("layanan AI tidak mengembalikan jawaban")
	}
	return msg, u, nil
}

// apiError menerjemahkan error layanan menjadi pesan yang mudah dipahami.
// Body error bisa berupa objek {"error":{...}} atau array berisi objek itu.
func apiError(status int, body []byte, withFormat bool) error {
	msg := errorMessage(body)
	lower := strings.ToLower(msg)
	switch {
	case status == http.StatusUnauthorized,
		strings.Contains(lower, "api key not valid"), strings.Contains(lower, "invalid api key"),
		strings.Contains(lower, "invalid x-api-key"), strings.Contains(lower, "incorrect api key"):
		return ErrInvalidKey
	case withFormat && status == http.StatusBadRequest &&
		(strings.Contains(lower, "response_format") || strings.Contains(lower, "json_schema")):
		return errNoFormat
	case status == http.StatusForbidden:
		return errors.New("API key tidak punya akses ke model ini")
	case status == http.StatusNotFound:
		return errors.New("model atau Base URL tidak ditemukan; periksa Pengaturan AI (tombol p)")
	case status == http.StatusTooManyRequests:
		return errors.New("batas kuota atau rate limit tercapai; tunggu sebentar atau ganti ke model yang lebih ringan")
	case status == http.StatusServiceUnavailable, status == 529:
		return errBusy
	}
	if msg != "" {
		return fmt.Errorf("layanan AI error %d: %s", status, msg)
	}
	return fmt.Errorf("layanan AI error %d", status)
}

func errorMessage(body []byte) string {
	type apiErr struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var one apiErr
	if json.Unmarshal(body, &one) == nil && one.Error.Message != "" {
		return one.Error.Message
	}
	var many []apiErr
	if json.Unmarshal(body, &many) == nil && len(many) > 0 {
		return many[0].Error.Message
	}
	return strings.TrimSpace(string(body))
}
