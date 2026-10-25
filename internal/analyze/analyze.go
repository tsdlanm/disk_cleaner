package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Provider mengirim prompt ke sebuah layanan AI dan mengembalikan jawaban
// berupa JSON yang mengikuti schema.
type Provider interface {
	Complete(ctx context.Context, system, user string, schema map[string]any) (string, error)
}

// Action adalah saran untuk sebuah item.
type Action string

const (
	ActionDelete Action = "hapus"
	ActionReview Action = "cek"
	ActionKeep   Action = "simpan"
)

// Mention adalah item yang disebut AI di dalam jawabannya.
type Mention struct {
	Item
	Action Action
}

// Result adalah hasil analisis AI.
type Result struct {
	// Text adalah jawaban AI. Setiap penyebutan item ditulis sebagai penanda
	// [[id]]; gunakan Segments untuk memecahnya.
	Text     string
	Mentions map[int]Mention // id -> item yang disebut
}

// Segment adalah potongan jawaban: teks biasa, atau penyebutan item jika ID > 0.
type Segment struct {
	Text string
	ID   int
}

var mentionRe = regexp.MustCompile(`\[\[(\d+)\]\]`)

// Segments memecah jawaban menjadi teks dan penyebutan item sesuai urutan.
func (r *Result) Segments() []Segment {
	var out []Segment
	last := 0
	for _, loc := range mentionRe.FindAllStringSubmatchIndex(r.Text, -1) {
		if loc[0] > last {
			out = append(out, Segment{Text: r.Text[last:loc[0]]})
		}
		id, _ := strconv.Atoi(r.Text[loc[2]:loc[3]])
		out = append(out, Segment{ID: id})
		last = loc[1]
	}
	if last < len(r.Text) {
		out = append(out, Segment{Text: r.Text[last:]})
	}
	return out
}

// Pengingat yang ditambahkan ke permintaan ulang.
const (
	retryJSON     = "\n\nPENTING: jawab HANYA dengan satu objek JSON yang sesuai format (field \"answer\" dan \"mentions\"), tanpa teks lain di luar JSON."
	retryMentions = "\n\nPENTING: jawaban sebelumnya tidak menyebut satu pun folder atau file. Sebutkan SEMUA folder dan file yang relevan dari ringkasan di atas: di \"answer\" sebagai baris \"• [[id]] penjelasan\" di bawah judul kelompoknya, dan di \"mentions\" beserta aksi dan alasannya."
)

// Analyze mengirim snapshot ke provider dan membaca jawabannya. Tidak semua
// model patuh pada format, jadi permintaan diulang sekali dengan pengingat
// jika jawaban bukan JSON, atau jika tidak menyebut satu pun folder/file.
func Analyze(ctx context.Context, p Provider, s *Snapshot) (*Result, error) {
	raw, err := p.Complete(ctx, SystemPrompt, UserPrompt(s), resultSchema)
	if err != nil {
		return nil, err
	}
	res, err := Parse(raw, s)
	switch {
	case err != nil:
		raw, err2 := p.Complete(ctx, SystemPrompt, UserPrompt(s)+retryJSON, resultSchema)
		if err2 != nil {
			return nil, err2
		}
		if res, err2 := Parse(raw, s); err2 == nil {
			return res, nil
		}
		return nil, err
	case len(res.Mentions) == 0 && len(s.Items) > 1:
		raw, err2 := p.Complete(ctx, SystemPrompt, UserPrompt(s)+retryMentions, resultSchema)
		if err2 != nil {
			return res, nil // jawaban pertama tetap lebih baik daripada error
		}
		if res2, err2 := Parse(raw, s); err2 == nil && len(res2.Mentions) > 0 {
			return res2, nil
		}
	}
	return res, nil
}

// extractJSON mengambil objek JSON dari jawaban, termasuk jika dibungkus
// blok kode ```json atau diapit teks lain.
func extractJSON(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return raw
	}
	return raw[start : end+1]
}

// Parse membaca jawaban JSON AI. Penanda yang merujuk id tidak dikenal
// dibuang dari teks; item tanpa aksi yang valid dianggap "cek".
func Parse(raw string, s *Snapshot) (*Result, error) {
	var out struct {
		Answer   string `json:"answer"`
		Mentions []struct {
			ID     int    `json:"id"`
			Action string `json:"action"`
			Reason string `json:"reason"`
		} `json:"mentions"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return nil, fmt.Errorf("jawaban AI bukan JSON yang valid: %w", err)
	}
	actions := make(map[int]Action)
	for _, m := range out.Mentions {
		actions[m.ID] = validAction(Action(m.Action))
	}

	res := &Result{Mentions: make(map[int]Mention)}
	res.Text = mentionRe.ReplaceAllStringFunc(out.Answer, func(tok string) string {
		id, _ := strconv.Atoi(mentionRe.FindStringSubmatch(tok)[1])
		item, ok := s.Items[id]
		if !ok {
			return ""
		}
		res.Mentions[id] = Mention{Item: item, Action: validAction(actions[id])}
		return tok
	})

	// Sebagian model mengisi "mentions" tapi lupa menaruh penandanya di teks.
	// Item itu ditambahkan di bawah jawaban, dikelompokkan sesuai aksinya,
	// agar tetap bisa dipilih, dibuka, dan dihapus.
	groups := map[Action][]string{}
	for _, m := range out.Mentions {
		item, ok := s.Items[m.ID]
		if _, shown := res.Mentions[m.ID]; !ok || shown {
			continue
		}
		a := validAction(Action(m.Action))
		res.Mentions[m.ID] = Mention{Item: item, Action: a}
		groups[a] = append(groups[a], strings.TrimSpace(fmt.Sprintf("• [[%d]] %s", m.ID, m.Reason)))
	}
	for _, g := range []struct {
		a     Action
		title string
	}{{ActionDelete, "Aman dihapus:"}, {ActionReview, "Perlu dicek dulu:"}, {ActionKeep, "Sebaiknya disimpan:"}} {
		if len(groups[g.a]) > 0 {
			res.Text = strings.TrimRight(res.Text, "\n") + "\n\n" + g.title + "\n" + strings.Join(groups[g.a], "\n")
		}
	}
	return res, nil
}

// validAction mengembalikan a jika dikenal; aksi lain dianggap "cek".
func validAction(a Action) Action {
	if a == ActionDelete || a == ActionReview || a == ActionKeep {
		return a
	}
	return ActionReview
}
