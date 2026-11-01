package analyze

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
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
	Action Action // kosong jika AI tidak menyebut aksi (jawaban chat)
	Reason string // alasan singkat dari AI, bila ada
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
const retryJSON = "\n\nPENTING: jawab HANYA dengan satu objek JSON yang sesuai format (field \"summary\" dan \"items\"), tanpa teks lain di luar JSON."

// retryMissing meminta penilaian untuk item yang terlewat.
func retryMissing(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return "\n\nPENTING: jawaban sebelumnya belum menilai item dengan id berikut: " + strings.Join(parts, ", ") +
		". Isi \"items\" dengan penilaian untuk SETIAP id tersebut."
}

// Analyze mengirim snapshot ke provider dan membaca jawabannya. Jawaban AI
// berupa JSON tetap (ringkasan + penilaian per item); teks yang tampil disusun
// aplikasi, jadi formatnya tidak bergantung pada kepatuhan model. Permintaan
// diulang sekali jika jawaban bukan JSON, dan sekali lagi untuk item wajib
// yang belum dinilai.
func Analyze(ctx context.Context, p Provider, s *Snapshot) (*Result, error) {
	user := UserPrompt(s)
	raw, err := p.Complete(ctx, SystemPrompt, user, resultSchema)
	if err != nil {
		return nil, err
	}
	a, err := decode(raw)
	if err != nil {
		raw, err2 := p.Complete(ctx, SystemPrompt, user+retryJSON, resultSchema)
		if err2 != nil {
			return nil, err2
		}
		if a, err2 = decode(raw); err2 != nil {
			return nil, err
		}
	}
	if miss := missing(s, a); len(miss) > 0 {
		if raw, err := p.Complete(ctx, SystemPrompt, user+retryMissing(miss), resultSchema); err == nil {
			if b, err := decode(raw); err == nil {
				// Penilaian pertama tetap dipakai; yang kedua hanya mengisi yang kurang.
				a.Items = append(a.Items, b.Items...)
				if strings.TrimSpace(a.Summary) == "" {
					a.Summary = b.Summary
				}
			}
		}
	}
	return build(a, s), nil
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

// answer adalah jawaban JSON AI.
type answer struct {
	Summary string `json:"summary"`
	Items   []struct {
		ID     int    `json:"id"`
		Action string `json:"action"`
		Reason string `json:"reason"`
	} `json:"items"`
}

func decode(raw string) (*answer, error) {
	var a answer
	if err := json.Unmarshal([]byte(extractJSON(raw)), &a); err != nil {
		return nil, fmt.Errorf("jawaban AI bukan JSON yang valid: %w", err)
	}
	return &a, nil
}

// wanted adalah id yang wajib dinilai. Tanpa daftar kandidat (snapshot lama
// atau buatan tes), semua item di ringkasan boleh dinilai.
func (s *Snapshot) wanted() map[int]bool {
	out := make(map[int]bool)
	for _, c := range s.Candidates {
		out[c.ID] = true
	}
	if len(out) == 0 {
		for id := range s.Items {
			out[id] = true
		}
	}
	return out
}

// missing mengembalikan id kandidat yang belum dinilai AI, urut dari yang terbesar.
func missing(s *Snapshot, a *answer) []int {
	got := make(map[int]bool)
	for _, it := range a.Items {
		got[it.ID] = true
	}
	var out []int
	for _, c := range s.Candidates {
		if !got[c.ID] {
			out = append(out, c.ID)
		}
	}
	return out
}

// Parse membaca jawaban JSON AI tanpa meminta ulang.
func Parse(raw string, s *Snapshot) (*Result, error) {
	a, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return build(a, s), nil
}

// noteMissing adalah alasan untuk item wajib yang tetap tidak dinilai AI.
const noteMissing = "AI tidak memberi penilaian untuk item ini; cek sendiri."

// build menyusun hasil dari jawaban AI: ringkasan, lalu kelompok "Aman
// dihapus", "Perlu dicek dulu", dan "Sebaiknya disimpan", masing-masing dari
// yang terbesar, satu butir "• [[id]] alasan" per item. Id yang tidak diminta
// diabaikan; kandidat yang tidak dinilai masuk "Perlu dicek dulu".
func build(a *answer, s *Snapshot) *Result {
	want := s.wanted()
	res := &Result{Mentions: make(map[int]Mention)}
	for _, it := range a.Items {
		item, ok := s.Items[it.ID]
		if _, seen := res.Mentions[it.ID]; !ok || seen || !want[it.ID] {
			continue
		}
		res.Mentions[it.ID] = Mention{Item: item, Action: validAction(Action(strings.ToLower(strings.TrimSpace(it.Action)))), Reason: clean(it.Reason)}
	}
	for _, c := range s.Candidates {
		if _, ok := res.Mentions[c.ID]; !ok {
			res.Mentions[c.ID] = Mention{Item: s.Items[c.ID], Action: ActionReview, Reason: noteMissing}
		}
	}

	var b strings.Builder
	b.WriteString(clean(a.Summary))
	for _, g := range []struct {
		a     Action
		title string
	}{{ActionDelete, "Aman dihapus:"}, {ActionReview, "Perlu dicek dulu:"}, {ActionKeep, "Sebaiknya disimpan:"}} {
		var group []Mention
		for _, mn := range res.Mentions {
			if mn.Action == g.a {
				group = append(group, mn)
			}
		}
		if len(group) == 0 {
			continue
		}
		slices.SortFunc(group, func(x, y Mention) int {
			if c := cmp.Compare(y.Size, x.Size); c != 0 {
				return c
			}
			return cmp.Compare(x.ID, y.ID)
		})
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(g.title)
		for _, mn := range group {
			fmt.Fprintf(&b, "\n• [[%d]] %s", mn.ID, mn.Reason)
		}
	}
	res.Text = strings.TrimRight(b.String(), " ")
	return res
}

// clean merapikan teks dari AI menjadi satu baris tanpa penanda [[id]].
func clean(s string) string {
	s = strings.Join(strings.Fields(chatMentionRe.ReplaceAllString(s, "")), " ")
	return strings.NewReplacer(" .", ".", " ,", ",", " ;", ";").Replace(s)
}

var (
	headingRe = regexp.MustCompile(`[ \t]*\n*[ \t]*(Aman dihapus|Perlu dicek dulu|Sebaiknya disimpan):[ \t]*\n*`)
	bulletRe  = regexp.MustCompile(`([^\n \t])[ \t]*•[ \t]*`)
)

// tidyAnswer memulihkan baris baru yang hilang: sebagian model menulis seluruh
// jawaban sebagai satu paragraf ("… Aman dihapus: • a • b Perlu dicek dulu: …").
// Judul kelompok dipisah baris kosong dan tiap butir "•" mulai di baris baru.
// Jawaban yang sudah rapi tidak berubah.
func tidyAnswer(s string) string {
	s = headingRe.ReplaceAllString(s, "\n\n$1:\n")
	s = bulletRe.ReplaceAllString(s, "$1\n• ")
	return strings.TrimLeft(s, "\n")
}

// validAction mengembalikan a jika dikenal; aksi lain dianggap "cek".
func validAction(a Action) Action {
	if a == ActionDelete || a == ActionReview || a == ActionKeep {
		return a
	}
	return ActionReview
}
