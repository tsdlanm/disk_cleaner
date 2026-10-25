package analyze

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

// Config adalah pengaturan aplikasi, disimpan di %APPDATA%\scan\config.json.
// Bagian AI (provider, Base URL, model, API key) diatur dari layar Pengaturan AI.
type Config struct {
	Provider  string `json:"provider"`    // "gemini", "claude", atau "custom"
	BaseURL   string `json:"base_url"`    // hanya untuk "custom"; preset memakai Base URL bawaan
	Model     string `json:"model"`       // model untuk provider itu
	APIKey    string `json:"api_key"`     // API key untuk provider itu
	MaxLines  int    `json:"max_lines"`   // batas baris ringkasan
	MinSizeMB int64  `json:"min_size_mb"` // batas bawah item yang dirinci

	RecentDays  int   `json:"recent_days"`   // rentang "baru ditambahkan" di layar utama
	RecentMinKB int64 `json:"recent_min_kb"` // file baru di bawah ini tidak didaftar (tetap dihitung)
	RescanHours int   `json:"rescan_hours"`  // hasil pemindaian tersimpan dipakai ulang selama ini, lalu dipindai ulang otomatis
}

// Preset adalah layanan AI yang sudah dikenal. Semua dipanggil lewat API
// format OpenAI (chat completions).
type Preset struct {
	Label    string
	BaseURL  string   // kosong untuk "custom": diisi pengguna
	Models   []string // pilihan model; yang pertama adalah bawaan
	Fallback string   // model cadangan saat model utama sibuk
	Info     string   // petunjuk di layar pengaturan
}

// DefaultGeminiModel adalah model Gemini bawaan; tersedia di tier gratis.
const DefaultGeminiModel = "gemini-3.8-flash"

// Providers adalah urutan pilihan provider di layar pengaturan.
var Providers = []string{"gemini", "claude", "custom"}

// Presets berisi pengaturan bawaan setiap provider.
var Presets = map[string]Preset{
	"gemini": {
		Label:    "Gemini",
		BaseURL:  "https://generativelanguage.googleapis.com/v1beta/openai/",
		Models:   []string{DefaultGeminiModel, "gemini-3.6-flash", "gemini-3.5-flash-lite", "gemini-3.1-flash-lite"},
		Fallback: "gemini-3.5-flash-lite",
		Info:     "Gemini punya tier gratis. Buat API key di aistudio.google.com/apikey",
	},
	"claude": {
		Label:   "Claude",
		BaseURL: "https://api.anthropic.com/v1/",
		Models:  []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"},
		Info:    "Buat API key di console.anthropic.com",
	},
	"custom": {
		Label: "Lainnya (OpenAI-compatible)",
		Info:  "Layanan lain berformat OpenAI: OpenRouter, Groq, DeepSeek, Ollama, dll. Isi Base URL dan nama model",
	},
}

// DefaultConfig adalah pengaturan awal jika file konfigurasi belum ada.
var DefaultConfig = Config{
	Provider:  "gemini",
	Model:     DefaultGeminiModel,
	MaxLines:  DefaultLimits.MaxLines,
	MinSizeMB: DefaultLimits.MinSize >> 20,

	RecentDays:  7,
	RecentMinKB: 1024,
	RescanHours: 6,
}

// ConfigPath mengembalikan lokasi file konfigurasi.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "scan", "config.json"), nil
}

// LoadConfig membaca konfigurasi. File yang belum ada bukan error.
func LoadConfig() (Config, error) {
	cfg := DefaultConfig
	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig, err
	}
	if !slices.Contains(Providers, cfg.Provider) {
		// Provider lama yang sudah tidak ada: kembali ke bawaan.
		cfg.SetProvider(DefaultConfig.Provider)
	}
	// Key/URL yang tersimpan sebelum ada pembersihan bisa berisi karakter
	// tak terlihat dari paste; bersihkan agar tetap bisa dipakai.
	cfg.APIKey, cfg.BaseURL = CleanToken(cfg.APIKey), CleanToken(cfg.BaseURL)
	return cfg, nil
}

// CleanToken membuang spasi dan karakter kontrol (misalnya NUL atau baris
// baru yang ikut tertempel) dari API key atau URL.
func CleanToken(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// Save menyimpan konfigurasi ke file milik pengguna ini.
func (c Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// SetProvider memilih provider. Jika model yang ada bukan model provider itu,
// model diganti ke bawaan provider tersebut.
func (c *Config) SetProvider(p string) {
	c.Provider = p
	if models := Presets[p].Models; len(models) > 0 && !slices.Contains(models, c.Model) {
		c.Model = models[0]
	}
}

// NeedsKey melaporkan apakah API key belum diisi.
func (c Config) NeedsKey() bool {
	return c.APIKey == ""
}

// ModelName mengembalikan model yang dipakai.
func (c Config) ModelName() string {
	if c.Model == "" && len(Presets[c.Provider].Models) > 0 {
		return Presets[c.Provider].Models[0]
	}
	return c.Model
}

// URL mengembalikan Base URL yang dipakai: bawaan preset, atau isian
// pengguna untuk "custom".
func (c Config) URL() string {
	if u := Presets[c.Provider].BaseURL; u != "" {
		return u
	}
	return c.BaseURL
}

// Limits mengembalikan batas ringkasan dari konfigurasi.
func (c Config) Limits() Limits {
	return Limits{MaxLines: c.MaxLines, MinSize: c.MinSizeMB << 20}
}

// NewProvider membuat Provider sesuai konfigurasi.
func (c Config) NewProvider() (Provider, error) {
	switch {
	case !slices.Contains(Providers, c.Provider):
		return nil, errors.New("provider tidak dikenal: " + c.Provider)
	case c.URL() == "":
		return nil, errors.New("Base URL belum diisi; buka Pengaturan AI (tombol p)")
	case c.ModelName() == "":
		return nil, errors.New("model belum diisi; buka Pengaturan AI (tombol p)")
	case c.APIKey == "":
		return nil, errors.New("API key belum diisi; buka Pengaturan AI (tombol p)")
	}
	return NewOpenAI(c.URL(), c.APIKey, c.ModelName(), Presets[c.Provider].Fallback), nil
}
