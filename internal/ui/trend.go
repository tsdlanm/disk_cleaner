package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/tsdlanm/cli_app/internal/format"
	"github.com/tsdlanm/cli_app/internal/recent"
	"github.com/tsdlanm/cli_app/internal/sizer"
	"github.com/tsdlanm/cli_app/internal/trend"
)

// maxTrendRows adalah jumlah folder terbanyak yang tampil di bagian Tren.
const maxTrendRows = 10

// recordTrend mencatat ukuran folder hasil pemindaian ke riwayat tren dan
// menyimpannya ke disk di background.
func (m *model) recordTrend(at time.Time, dirs map[string]sizer.Info) tea.Cmd {
	if m.trendHist == nil {
		m.trendHist = &trend.History{}
	}
	if !m.trendHist.Add(at, dirSizes(dirs)) {
		return nil
	}
	m.trendRows = nil
	base := dataDir()
	if base == "" {
		return nil
	}
	cp := &trend.History{Points: slices.Clone(m.trendHist.Points)}
	return func() tea.Msg {
		_ = cp.Save(trend.Path(base))
		return nil
	}
}

func dirSizes(dirs map[string]sizer.Info) map[string]int64 {
	out := make(map[string]int64, len(dirs))
	for p, inf := range dirs {
		out[p] = inf.Size
	}
	return out
}

// trendData menghitung folder yang bertambah untuk rentang yang dipilih.
// ok false jika riwayat belum cukup panjang.
func (m *model) trendData() (rows []trend.Growth, base time.Time, ok bool) {
	if m.trendDone {
		return m.trendRows, m.trendBase, m.trendOK
	}
	m.trendDone = true
	m.trendRows, m.trendOK = nil, false
	if m.trendHist == nil {
		return nil, time.Time{}, false
	}
	cur, old, ok := m.trendHist.Base(m.trange)
	if !ok {
		return nil, time.Time{}, false
	}
	m.trendRows = trend.Compare(old, cur, maxTrendRows)
	m.trendBase, m.trendOK = old.Time, true
	return m.trendRows, m.trendBase, true
}

// setTrendRange memilih rentang Tren.
func (m *model) setTrendRange(d time.Duration) {
	m.trange = d
	m.trendDone = false
	m.tcursor = 0
}

// trendSelected mengembalikan folder Tren di kursor. Kursor dirapikan jika
// daftarnya berubah atau menyusut.
func (m *model) trendSelected() (trend.Growth, bool) {
	rows, _, _ := m.trendData()
	n := min(len(rows), m.trendShown)
	if n == 0 {
		m.trendFocus = false
		return trend.Growth{}, false
	}
	m.tcursor = min(max(m.tcursor, 0), n-1)
	return rows[m.tcursor], true
}

// handleTrendKey menangani tombol di halaman Drive: pilihan rentang Tren, dan
// pemilihan baris Tren (↓ dari drive terakhir masuk ke daftar Tren).
func (m *model) handleTrendKey(key string) (tea.Cmd, bool) {
	if !m.trendFocus && m.trendShown > 0 && len(m.drives) > 0 && m.cursor == len(m.drives)-1 &&
		(key == "down" || key == "j") {
		m.trendFocus, m.tcursor = true, 0
		return nil, true
	}
	if m.trendFocus {
		g, ok := m.trendSelected()
		if !ok {
			return nil, false
		}
		n := min(len(m.trendRows), m.trendShown)
		switch key {
		case "up", "k":
			if m.tcursor == 0 {
				m.trendFocus = false
				m.cursor = max(len(m.drives)-1, 0)
			} else {
				m.tcursor--
			}
		case "down", "j":
			if m.tcursor >= n-1 {
				m.trendFocus, m.cursor = false, 0
			} else {
				m.tcursor++
			}
		case "pgup", "home", "g":
			m.tcursor = 0
		case "pgdown", "end", "G":
			m.tcursor = n - 1
		case "esc", "left", "h":
			m.trendFocus = false
			m.cursor = max(len(m.drives)-1, 0)
		case "enter", "right", "l":
			if !m.dirExists(g.Path) {
				m.status = tildePath(g.Path) + " sudah tidak ada"
				return nil, true
			}
			return loadDir(g.Path), true
		case "o":
			m.status = runExplorer(`explorer.exe "` + g.Path + `"`)
		case "a":
			return m.analyzePath(g.Path), true
		case "t", "1", "7", "3":
		default:
			return nil, false
		}
		if key != "t" && key != "1" && key != "7" && key != "3" {
			return nil, true
		}
	}
	switch key {
	case "t":
		m.setTrendRange(nextRange(m.trange))
	case "1":
		m.setTrendRange(rangeDay)
	case "7":
		m.setTrendRange(rangeWeek)
	case "3":
		m.setTrendRange(rangeMonth)
	default:
		return nil, false
	}
	return nil, true
}

// viewTrend menulis bagian Tren di bawah daftar drive dalam paling banyak h
// baris. Mengembalikan jumlah baris yang ditulis.
func (m *model) viewTrend(b *strings.Builder, h int) int {
	m.trendShown = 0
	if h < 3 {
		return 0
	}
	used := 0
	line := func(s string) {
		m.line(b, s)
		used++
	}
	b.WriteString("\n")
	used++
	var chips []string
	for _, d := range []time.Duration{rangeDay, rangeWeek, rangeMonth} {
		if d == m.trange {
			chips = append(chips, selStyle.Render(" "+rangeLabel(d)+" "))
		} else {
			chips = append(chips, dimStyle.Render(" "+rangeLabel(d)+" "))
		}
	}
	head := titleStyle.Render("Tren") + "  " + strings.Join(chips, "")
	rows, base, ok := m.trendData()
	if ok {
		head += dimStyle.Render(" · folder yang bertambah dibanding " + relTime(base))
	}
	line(head)

	switch {
	case !ok:
		msg := " Belum ada riwayat untuk dibandingkan."
		if m.trendHist != nil && len(m.trendHist.Points) > 0 {
			pts := m.trendHist.Points
			msg = fmt.Sprintf(" Belum cukup riwayat untuk %s: tercatat sejak %s, terakhir %s. Buka aplikasi lagi nanti.",
				rangeLabel(m.trange), relTime(pts[0].Time), relTime(pts[len(pts)-1].Time))
		}
		line(dimStyle.Render(msg))
	case len(rows) == 0:
		line(dimStyle.Render(" Tidak ada folder yang bertambah ≥ " + format.Bytes(trend.MinDelta) + "."))
	default:
		const deltaW, sizeW = 11, 10
		pathW := max(m.screenWidth()-3-deltaW-2-sizeW-2, 10)
		n := min(len(rows), h-used)
		m.trendShown = n
		for i, g := range rows[:n] {
			path := runewidth.FillRight(truncateLeft(tildePath(g.Path), pathW), pathW)
			if m.trendFocus && i == m.tcursor {
				path = selStyle.Render(path)
			}
			line(" " + warnStyle.Render(fmt.Sprintf("%*s", deltaW, "+"+format.Bytes(g.Delta))) + "  " + path +
				"  " + dimStyle.Render(fmt.Sprintf("%*s", sizeW, format.Bytes(g.Size))))
		}
	}
	return used
}

// seedTrend mengisi riwayat kosong dari hasil tersimpan, supaya titik
// pembanding pertama sudah ada tanpa menunggu pemindaian berikutnya.
func (m *model) seedTrend(s *recent.Snapshot) {
	if m.trendHist == nil {
		m.trendHist = &trend.History{}
	}
	if len(m.trendHist.Points) == 0 && len(s.Dirs) > 0 {
		m.trendHist.Add(s.Time, dirSizes(s.Dirs))
	}
	m.trendDone = false
}
