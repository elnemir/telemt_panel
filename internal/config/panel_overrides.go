package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const PanelOverridesMaxBytes = 8 << 10
const PanelOverridesName = "panel-overrides.toml"

// Only these startup settings may be supplied by the panel editor.
type panelOverrides struct {
	Auth struct {
		SessionTTL *string `toml:"session_ttl,omitempty"`
	} `toml:"auth,omitempty"`
	Geography struct {
		BuildTimeoutSecs *int64 `toml:"build_timeout_secs,omitempty"`
	} `toml:"geography,omitempty"`
	CustomerPortal struct {
		Enabled *bool `toml:"enabled,omitempty"`
		Email   struct {
			Enabled *bool `toml:"enabled,omitempty"`
		} `toml:"email,omitempty"`
		Telegram struct {
			Enabled *bool `toml:"enabled,omitempty"`
		} `toml:"telegram,omitempty"`
	} `toml:"customer_portal,omitempty"`
}

// ReadPanelOverrides treats an absent file as an empty override.
func ReadPanelOverrides(dataDir string) ([]byte, error) {
	if dataDir == "" {
		return nil, nil
	}
	path := filepath.Join(dataDir, PanelOverridesName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("panel overrides must be a readable regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("could not open panel overrides")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("panel overrides file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, PanelOverridesMaxBytes+1))
	if err != nil || len(raw) > PanelOverridesMaxBytes {
		return nil, errors.New("panel overrides are unreadable or too large")
	}
	return raw, nil
}

// ApplyPanelOverrides returns an effective config without mutating the source.
// Decode and validation errors may be shown to the operator, so only fixed
// field labels and sanitized TOML diagnostics may leave this function.
func ApplyPanelOverrides(base *Config, raw []byte) (*Config, error) {
	if len(raw) > PanelOverridesMaxBytes {
		return nil, errors.New("panel settings TOML is too large")
	}
	var override panelOverrides
	md, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&override)
	if err != nil {
		return nil, sourceParseError(err)
	}
	if len(md.Undecoded()) != 0 {
		return nil, errors.New("one or more fields cannot be edited here")
	}
	return (PanelSettingsPatch{
		SessionTTL: override.Auth.SessionTTL, MapTimeoutSecs: override.Geography.BuildTimeoutSecs,
		CustomerPortalEnabled: override.CustomerPortal.Enabled, EmailEnabled: override.CustomerPortal.Email.Enabled,
		TelegramEnabled: override.CustomerPortal.Telegram.Enabled,
	}).Apply(base)
}

// PanelSettings contains only the fields exposed by the browser form.
type PanelSettings struct {
	SessionTTL            string `json:"session_ttl"`
	MapTimeoutSecs        int64  `json:"map_timeout_secs"`
	CustomerPortalEnabled bool   `json:"customer_portal_enabled"`
	EmailEnabled          bool   `json:"email_enabled"`
	TelegramEnabled       bool   `json:"telegram_enabled"`
}

func EditablePanelSettings(cfg *Config) PanelSettings {
	return PanelSettings{
		SessionTTL: cfg.Auth.SessionTTLDuration().String(), MapTimeoutSecs: cfg.Geography.BuildTimeoutSecs,
		CustomerPortalEnabled: cfg.CustomerPortal.Enabled, EmailEnabled: cfg.CustomerPortal.Email.Enabled,
		TelegramEnabled: cfg.CustomerPortal.Telegram.Enabled,
	}
}

// PanelSettingsPatch inherits omitted values from the startup configuration.
type PanelSettingsPatch struct {
	SessionTTL            *string `json:"session_ttl,omitempty"`
	MapTimeoutSecs        *int64  `json:"map_timeout_secs,omitempty"`
	CustomerPortalEnabled *bool   `json:"customer_portal_enabled,omitempty"`
	EmailEnabled          *bool   `json:"email_enabled,omitempty"`
	TelegramEnabled       *bool   `json:"telegram_enabled,omitempty"`
}

type PanelSettingsFieldError struct {
	Field string
	err   error
}

func (e *PanelSettingsFieldError) Error() string { return e.err.Error() }

func (p PanelSettingsPatch) Apply(base *Config) (*Config, error) {
	next := *base
	if v := p.SessionTTL; v != nil {
		d, err := time.ParseDuration(*v)
		if err != nil || d <= 0 {
			return nil, &PanelSettingsFieldError{"session_ttl", errors.New("session duration must be positive, for example 48h")}
		}
		next.Auth.SessionTTL = d.String()
	}
	if v := p.MapTimeoutSecs; v != nil {
		if *v < 1 || *v > 120 {
			return nil, &PanelSettingsFieldError{"map_timeout_secs", errors.New("map timeout must be between 1 and 120 seconds")}
		}
		next.Geography.BuildTimeoutSecs = *v
	}
	if v := p.CustomerPortalEnabled; v != nil {
		next.CustomerPortal.Enabled = *v
	}
	if v := p.EmailEnabled; v != nil {
		next.CustomerPortal.Email.Enabled = *v
	}
	if v := p.TelegramEnabled; v != nil {
		next.CustomerPortal.Telegram.Enabled = *v
	}
	if err := next.CustomerPortal.Validate(next.BasePath, next.Platform.Enabled, next.Agent.Enabled); err != nil {
		field := "customer_portal_enabled"
		if strings.HasPrefix(err.Error(), "customer_portal.email") {
			field = "email_enabled"
		}
		if strings.HasPrefix(err.Error(), "customer_portal.telegram") {
			field = "telegram_enabled"
		}
		return nil, &PanelSettingsFieldError{field, err}
	}
	return &next, nil
}

// EncodePanelOverrides stores differences only. Changes to unrelated fields
// in the startup TOML continue to take effect on the next restart.
func EncodePanelOverrides(base, next *Config) ([]byte, error) {
	var override panelOverrides
	if base.Auth.SessionTTLDuration() != next.Auth.SessionTTLDuration() {
		v := next.Auth.SessionTTLDuration().String()
		override.Auth.SessionTTL = &v
	}
	if base.Geography.BuildTimeoutSecs != next.Geography.BuildTimeoutSecs {
		override.Geography.BuildTimeoutSecs = &next.Geography.BuildTimeoutSecs
	}
	if base.CustomerPortal.Enabled != next.CustomerPortal.Enabled {
		override.CustomerPortal.Enabled = &next.CustomerPortal.Enabled
	}
	if base.CustomerPortal.Email.Enabled != next.CustomerPortal.Email.Enabled {
		override.CustomerPortal.Email.Enabled = &next.CustomerPortal.Email.Enabled
	}
	if base.CustomerPortal.Telegram.Enabled != next.CustomerPortal.Telegram.Enabled {
		override.CustomerPortal.Telegram.Enabled = &next.CustomerPortal.Telegram.Enabled
	}
	var out bytes.Buffer
	err := toml.NewEncoder(&out).Encode(override)
	return out.Bytes(), err
}
