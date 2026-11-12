package subpage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const settingsKey = "subscription.appearance.v1"

var (
	ErrSettingsConflict = errors.New("subscription settings changed")
	ErrSettingsInvalid  = errors.New("invalid subscription settings")
)

type PageCopy struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	SupportLabel string `json:"support_label"`
}

type Appearance struct {
	Language    string   `json:"language"`
	Theme       string   `json:"theme"`
	Accent      string   `json:"accent"`
	Logo        string   `json:"logo"`
	RU          PageCopy `json:"ru"`
	EN          PageCopy `json:"en"`
	SupportURL  string   `json:"support_url"`
	Hidden      []string `json:"hidden"`
	Connections []string `json:"connections"`
}

type AppearanceSnapshot struct {
	Settings Appearance `json:"settings"`
	Revision string     `json:"revision"`
}

type appearanceStore interface {
	GetSetting(string) (string, bool, error)
	SetSetting(string, string) error
}

// AppearanceSettings serializes edits against the revision returned by Read.
type AppearanceSettings struct {
	mu sync.Mutex
	st appearanceStore
}

func NewAppearanceSettings(st appearanceStore) *AppearanceSettings {
	return &AppearanceSettings{st: st}
}

func DefaultAppearance() Appearance {
	return Appearance{Language: "auto", Theme: "auto", Accent: "blue", Logo: "panel",
		Hidden: []string{}, Connections: []string{"tls", "secure", "classic", "web"}}
}

func NormalizeAppearance(c Appearance) (Appearance, error) {
	if !slices.Contains([]string{"auto", "ru", "en"}, c.Language) ||
		!slices.Contains([]string{"auto", "light", "dark"}, c.Theme) ||
		!slices.Contains([]string{"blue", "emerald", "violet", "amber"}, c.Accent) ||
		!slices.Contains([]string{"initial", "panel", "hidden"}, c.Logo) {
		return Appearance{}, ErrSettingsInvalid
	}
	for _, copy := range []*PageCopy{&c.RU, &c.EN} {
		copy.Title = strings.TrimSpace(copy.Title)
		copy.Description = strings.TrimSpace(copy.Description)
		copy.Instructions = strings.TrimSpace(copy.Instructions)
		copy.SupportLabel = strings.TrimSpace(copy.SupportLabel)
		if !validCopy(copy.Title, 80, false) || !validCopy(copy.Description, 400, true) ||
			!validCopy(copy.Instructions, 1500, true) || !validCopy(copy.SupportLabel, 60, false) {
			return Appearance{}, ErrSettingsInvalid
		}
	}
	c.SupportURL = strings.TrimSpace(c.SupportURL)
	if c.SupportURL != "" {
		u, err := url.Parse(c.SupportURL)
		if err != nil || len(c.SupportURL) > 512 || u.Scheme != "https" || u.Hostname() == "" ||
			u.User != nil || strings.ContainsAny(c.SupportURL, "\\") || strings.ContainsFunc(c.SupportURL, unicode.IsSpace) {
			return Appearance{}, ErrSettingsInvalid
		}
	}
	if !validOptions(c.Hidden, []string{"status", "quota", "expiry", "qr", "manual", "instructions", "fallback"}) ||
		len(c.Connections) == 0 || !validOptions(c.Connections, DefaultAppearance().Connections) {
		return Appearance{}, ErrSettingsInvalid
	}
	c.Hidden = append([]string{}, c.Hidden...)
	c.Connections = slices.Clone(c.Connections)
	slices.Sort(c.Hidden)
	slices.Sort(c.Connections)
	return c, nil
}

func validOptions(values, allowed []string) bool {
	if len(values) > len(allowed) {
		return false
	}
	for i, value := range values {
		if !slices.Contains(allowed, value) || slices.Contains(values[:i], value) {
			return false
		}
	}
	return true
}

func validCopy(value string, limit int, multiline bool) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit &&
		!strings.ContainsFunc(value, func(r rune) bool {
			return unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t'))
		})
}

func snapshot(c Appearance) (AppearanceSnapshot, string) {
	raw, _ := json.Marshal(c)
	digest := sha256.Sum256(raw)
	return AppearanceSnapshot{Settings: c, Revision: hex.EncodeToString(digest[:])}, string(raw)
}

func (s *AppearanceSettings) Read() (AppearanceSnapshot, error) {
	raw, found, err := s.st.GetSetting(settingsKey)
	if err != nil {
		return AppearanceSnapshot{}, err
	}
	c := DefaultAppearance()
	if found {
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return AppearanceSnapshot{}, err
		}
	}
	c, err = NormalizeAppearance(c)
	if err != nil {
		return AppearanceSnapshot{}, err
	}
	result, _ := snapshot(c)
	return result, nil
}

func (s *AppearanceSettings) Save(c Appearance, expected string) (AppearanceSnapshot, error) {
	c, err := NormalizeAppearance(c)
	if err != nil {
		return AppearanceSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Read()
	if err != nil {
		return AppearanceSnapshot{}, err
	}
	if expected != current.Revision {
		return AppearanceSnapshot{}, ErrSettingsConflict
	}
	next, raw := snapshot(c)
	if next.Revision != current.Revision {
		if err := s.st.SetSetting(settingsKey, raw); err != nil {
			return AppearanceSnapshot{}, err
		}
	}
	return next, nil
}
