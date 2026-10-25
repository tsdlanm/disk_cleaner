package ui

import (
	"slices"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsdlanm/cli_app/internal/analyze"
)

// settings adalah layar Pengaturan AI: pilih provider, lalu model, lalu
// isi API key. Perubahan disimpan saat layar ditutup.
type settings struct {
	cfg     analyze.Config // salinan yang sedang diubah
	row     int            // indeks ke fields()
	editing bool           // sedang mengetik
	buf     []rune
	then    string // folder yang langsung dianalisis setelah disimpan (dari tombol a)
}

// field adalah satu baris di layar pengaturan.
type field int

const (
	fieldProvider field = iota
	fieldBaseURL        // hanya untuk provider "custom"
	fieldModel
	fieldKey
)

// openSettings membuka Pengaturan AI. then berisi folder yang dianalisis
// setelah pengaturan disimpan; editKey langsung membuka input API key.
func (m *model) openSettings(then string, editKey bool) {
	s := &settings{cfg: m.cfg, then: then}
	if editKey {
		s.row = slices.Index(s.fields(), fieldKey)
		s.editing = true
	}
	m.settings = s
}

// fields mengembalikan baris yang tampil untuk provider yang dipilih.
func (s *settings) fields() []field {
	if s.cfg.Provider == "custom" {
		return []field{fieldProvider, fieldBaseURL, fieldModel, fieldKey}
	}
	return []field{fieldProvider, fieldModel, fieldKey}
}

func (s *settings) current() field { return s.fields()[s.row] }

func (m *model) handleSettingsKey(msg tea.KeyMsg) tea.Cmd {
	s := m.settings
	if msg.Type == tea.KeyCtrlC {
		return tea.Quit
	}
	if s.editing {
		switch msg.Type {
		case tea.KeyEsc:
			s.editing, s.buf = false, nil
		case tea.KeyBackspace:
			if len(s.buf) > 0 {
				s.buf = s.buf[:len(s.buf)-1]
			}
		case tea.KeyCtrlU:
			s.buf = nil
		case tea.KeyRunes, tea.KeySpace:
			// Karakter kontrol (misalnya NUL dari Ctrl+Space atau baris baru
			// dari paste) tidak pernah dimasukkan.
			for _, r := range msg.Runes {
				if !unicode.IsControl(r) {
					s.buf = append(s.buf, r)
				}
			}
		case tea.KeyEnter:
			v := strings.TrimSpace(string(s.buf))
			s.editing, s.buf = false, nil
			switch s.current() {
			case fieldBaseURL:
				s.cfg.BaseURL = analyze.CleanToken(v)
			case fieldModel:
				s.cfg.Model = v
			case fieldKey:
				// API key dipakai apa adanya. Jika dibuka dari tombol a,
				// pengaturan langsung disimpan dan analisis dimulai.
				s.cfg.APIKey = analyze.CleanToken(v)
				if s.then != "" && !s.cfg.NeedsKey() {
					return m.closeSettings()
				}
			}
		}
		return nil
	}

	n := len(s.fields())
	switch msg.String() {
	case "esc", "q", "p":
		return m.closeSettings()
	case "up", "k":
		s.row = (s.row - 1 + n) % n
	case "down", "j", "tab":
		s.row = (s.row + 1) % n
	case "left", "h":
		s.cycle(-1)
	case "right", "l":
		s.cycle(1)
	case "enter":
		switch s.current() {
		case fieldProvider:
			s.cycle(1)
		case fieldBaseURL:
			s.editing, s.buf = true, []rune(s.cfg.BaseURL)
		case fieldModel:
			s.editing, s.buf = true, []rune(s.cfg.ModelName())
		case fieldKey:
			s.editing, s.buf = true, nil
		}
	}
	return nil
}

// cycle mengganti provider atau model ke pilihan berikutnya (d = 1) atau
// sebelumnya (d = -1).
func (s *settings) cycle(d int) {
	switch s.current() {
	case fieldProvider:
		i := slices.Index(analyze.Providers, s.cfg.Provider)
		n := len(analyze.Providers)
		s.cfg.SetProvider(analyze.Providers[((i+d)%n+n)%n])
	case fieldModel:
		models := analyze.Presets[s.cfg.Provider].Models
		if len(models) == 0 {
			return
		}
		i := slices.Index(models, s.cfg.ModelName()) // -1 untuk model ketikan sendiri
		n := len(models)
		s.cfg.Model = models[((i+d)%n+n)%n]
	}
}

// closeSettings menyimpan pengaturan, menutup layar, dan memulai analisis
// yang tertunda jika ada.
func (m *model) closeSettings() tea.Cmd {
	s := m.settings
	m.settings = nil
	m.cfg = s.cfg
	if err := m.cfg.Save(); err != nil {
		m.status = "Pengaturan tidak bisa disimpan, hanya dipakai sampai program ditutup: " + err.Error()
	} else {
		m.status = "Pengaturan AI disimpan: " + analyze.Presets[m.cfg.Provider].Label + " · " + m.cfg.ModelName()
	}
	if s.then != "" && !m.cfg.NeedsKey() {
		return m.startAnalysis(s.then)
	}
	return nil
}

func (m *model) settingsPopup() *popup {
	s := m.settings
	preset := analyze.Presets[s.cfg.Provider]
	edit := func(f field) bool { return s.editing && s.current() == f }

	var lines []string
	for i, f := range s.fields() {
		mark := "  "
		if i == s.row {
			mark = "› "
		}
		var label, value string
		switch f {
		case fieldProvider:
			label, value = "Provider", "‹ "+preset.Label+" ›"
		case fieldBaseURL:
			label, value = "Base URL", orEmpty(s.cfg.BaseURL)
			if edit(f) {
				value = string(s.buf) + "▏"
			}
		case fieldModel:
			label, value = "Model", orEmpty(s.cfg.ModelName())
			if len(preset.Models) > 0 {
				value = "‹ " + s.cfg.ModelName() + " ›"
			}
			if edit(f) {
				value = string(s.buf) + "▏"
			}
		case fieldKey:
			label, value = "API key", maskKey(s.cfg.APIKey)
			if s.cfg.APIKey == "" {
				value = "(belum diisi)"
			}
			if edit(f) {
				value = maskKey(string(s.buf)) + "▏"
			}
		}
		lines = append(lines, mark+padRight(label, 9)+": "+value)
	}
	path, _ := analyze.ConfigPath()
	lines = append(lines, "", preset.Info, "Disimpan di "+path)

	hint := "↑↓ pilih · ←→ ganti · enter ubah · esc simpan & tutup"
	if s.editing {
		hint = "tempel/ketik lalu enter · ctrl+u kosongkan · esc batal"
	}
	return &popup{title: "Pengaturan AI", lines: lines, hint: hint}
}

func orEmpty(s string) string {
	if s == "" {
		return "(belum diisi)"
	}
	return s
}

func padRight(s string, n int) string {
	return s + strings.Repeat(" ", max(n-len(s), 0))
}

// maskKey menampilkan sebagian kecil API key agar tidak terbaca utuh di layar.
func maskKey(k string) string {
	if len(k) <= 12 {
		return strings.Repeat("•", len(k))
	}
	return k[:4] + "…" + k[len(k)-4:]
}
