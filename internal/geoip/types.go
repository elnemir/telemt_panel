// Package geoip manages verified local MaxMind DB readers for panel-owned
// address enrichment. It never sends observed addresses to an external service.
package geoip

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
)

// Source identifies where GeoIP database files come from.
type Source string

const (
	// SourceCommunity is retained for previously saved configurations and bundles.
	SourceCommunity Source = "community"
	// SourceURLs and SourceFiles use locations supplied by the operator.
	SourceURLs    Source = "urls"
	SourceFiles   Source = "files"
	SourceMaxMind Source = "maxmind"
	SourceDBIP    Source = "dbip"
	SourceIPinfo  Source = "ipinfo"
)

func (s Source) managed() bool {
	return s == SourceMaxMind || s == SourceDBIP || s == SourceIPinfo
}

func (s Source) usesKey() bool {
	return s == SourceMaxMind || s == SourceIPinfo
}

// Schedule controls automatic GeoIP database refreshes.
type Schedule string

const (
	// ScheduleWeekly, ScheduleDaily, and ScheduleManual are supported refresh schedules.
	ScheduleWeekly Schedule = "weekly"
	ScheduleDaily  Schedule = "daily"
	ScheduleManual Schedule = "manual"
)

// Kind identifies the records supplied by a GeoIP database.
type Kind string

const (
	// KindCountry, KindASN, and KindCity are supported database kinds.
	KindCountry Kind = "country"
	KindASN     Kind = "asn"
	KindCity    Kind = "city"
)

const (
	// StateDisabled, StateEmpty, StateReady, StateUpdating, and StateError are manager states.
	StateDisabled = "disabled"
	StateEmpty    = "empty"
	StateReady    = "ready"
	StateUpdating = "updating"
	StateError    = "error"

	// ResultFound, ResultPrivate, and ResultNotFound are lookup result states.
	ResultFound    = "found"
	ResultPrivate  = "private"
	ResultNotFound = "not_found"
)

var (
	// ErrBusy reports an update already in progress.
	ErrBusy = errors.New("geoip operation already in progress")
	// ErrDisabled reports an update requested while GeoIP is disabled.
	ErrDisabled = errors.New("geoip is disabled")
	// ErrInvalidConfig reports invalid GeoIP configuration.
	ErrInvalidConfig = errors.New("invalid geoip configuration")
)

// DatabaseConfig selects and locates one database kind.
type DatabaseConfig struct {
	Enabled  bool   `json:"enabled"`
	Location string `json:"location"`
}

// Config is the persisted desired GeoIP configuration.
type Config struct {
	Enabled   bool           `json:"enabled"`
	Source    Source         `json:"source"`
	Schedule  Schedule       `json:"schedule"`
	Country   DatabaseConfig `json:"country"`
	ASN       DatabaseConfig `json:"asn"`
	City      DatabaseConfig `json:"city"`
	AccountID string         `json:"account_id,omitempty"`
	accessKey string
}

// DefaultConfig returns the initial disabled operator-URL configuration.
func DefaultConfig() Config {
	return Config{
		Source: SourceURLs, Schedule: ScheduleWeekly,
		Country: DatabaseConfig{Enabled: true},
		ASN:     DatabaseConfig{Enabled: true},
	}
}

// Validate checks configuration values without accessing their locations.
func (c Config) Validate() error {
	switch c.Source {
	case SourceCommunity, SourceURLs, SourceFiles, SourceMaxMind, SourceDBIP, SourceIPinfo:
	default:
		return fmt.Errorf("%w: unknown source", ErrInvalidConfig)
	}
	switch c.Schedule {
	case ScheduleWeekly, ScheduleDaily, ScheduleManual:
	default:
		return fmt.Errorf("%w: unknown schedule", ErrInvalidConfig)
	}
	if c.Enabled && !c.Country.Enabled && !c.ASN.Enabled && !c.City.Enabled {
		return fmt.Errorf("%w: select at least one database", ErrInvalidConfig)
	}
	if c.Source == SourceIPinfo && c.City.Enabled {
		return fmt.Errorf("%w: IPinfo Lite supplies country and ASN only", ErrInvalidConfig)
	}
	if c.Source == SourceMaxMind && (c.Enabled || c.AccountID != "") {
		n, err := strconv.ParseUint(c.AccountID, 10, 64)
		if err != nil || n == 0 || len(c.AccountID) > 20 {
			return fmt.Errorf("%w: MaxMind requires a numeric account ID", ErrInvalidConfig)
		}
	} else if c.AccountID != "" {
		return fmt.Errorf("%w: account ID only applies to MaxMind", ErrInvalidConfig)
	}
	if c.Source.usesKey() && (c.Enabled || c.accessKey != "") {
		if !validAccessKey(c.accessKey) {
			return fmt.Errorf("%w: provider access key is required", ErrInvalidConfig)
		}
	} else if c.accessKey != "" {
		return fmt.Errorf("%w: source does not use an access key", ErrInvalidConfig)
	}
	for _, database := range c.databases() {
		if c.Source.managed() {
			if database.config.Location != "" {
				return fmt.Errorf("%w: provider uses built-in download locations", ErrInvalidConfig)
			}
			continue
		}
		if !database.config.Enabled || c.Source == SourceCommunity {
			continue
		}
		if database.config.Location == "" {
			if !c.Enabled {
				continue
			}
			return fmt.Errorf("%w: %s location is required", ErrInvalidConfig, database.kind)
		}
		switch c.Source {
		case SourceURLs:
			u, err := url.Parse(database.config.Location)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
				return fmt.Errorf("%w: %s must be an HTTPS URL without userinfo or fragment", ErrInvalidConfig, database.kind)
			}
		case SourceFiles:
			if !filepath.IsAbs(database.config.Location) {
				return fmt.Errorf("%w: %s must be an absolute path", ErrInvalidConfig, database.kind)
			}
		}
	}
	return nil
}

func validAccessKey(key string) bool {
	if len(key) == 0 || len(key) > 512 {
		return false
	}
	for _, char := range key {
		if char < '!' || char > '~' {
			return false
		}
	}
	return true
}

type configuredDatabase struct {
	kind   Kind
	config DatabaseConfig
}

func (c Config) databases() []configuredDatabase {
	return []configuredDatabase{
		{kind: KindCountry, config: c.Country},
		{kind: KindASN, config: c.ASN},
		{kind: KindCity, config: c.City},
	}
}

// DatabaseStatus describes one database in the active bundle.
type DatabaseStatus struct {
	Kind            Kind                `json:"kind"`
	BuildEpochSecs  int64               `json:"build_epoch_secs"`
	LoadedEpochSecs int64               `json:"loaded_epoch_secs"`
	Provenance      *DatabaseProvenance `json:"provenance"`
}

// DatabaseProvenance records the acquired file, without authenticating its publisher.
// URL identities omit all query parameters, userinfo and fragments.
type DatabaseProvenance struct {
	OriginalLocation string `json:"original_location"`
	FinalLocation    string `json:"final_location"`
	FetchedEpochSecs int64  `json:"fetched_epoch_secs"`
	SHA256           string `json:"sha256"`
	DatabaseType     string `json:"database_type"`
}

// Status describes GeoIP availability and the active database bundle.
type Status struct {
	State        string           `json:"state"`
	Available    bool             `json:"available"`
	ActiveSource *Source          `json:"active_source"`
	Databases    []DatabaseStatus `json:"databases"`
	LastError    *string          `json:"last_error"`
}

// Settings combines desired configuration with runtime status.
type Settings struct {
	Config         Config `json:"config"`
	Status         Status `json:"status"`
	AccessKeySaved bool   `json:"access_key_saved,omitempty"`
}

// Result contains the local enrichment for one IP address.
type Result struct {
	State           string    `json:"state"`
	CountryCode     string    `json:"country_code"`
	CountryName     string    `json:"country_name"`
	CountryNameRU   string    `json:"country_name_ru"`
	City            string    `json:"city"`
	CityRU          string    `json:"city_ru"`
	ASN             uint      `json:"asn"`
	Organization    string    `json:"organization"`
	CityID          *uint32   `json:"city_id"`
	Location        *Location `json:"location"`
	CountryConflict bool      `json:"-"`
}

// Location describes an approximate network position from a City database.
type Location struct {
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	AccuracyRadiusKM *uint16 `json:"accuracy_radius_km"`
}

func cloneResult(result Result) *Result {
	if result.CityID != nil {
		id := *result.CityID
		result.CityID = &id
	}
	if result.Location != nil {
		location := *result.Location
		if location.AccuracyRadiusKM != nil {
			radius := *location.AccuracyRadiusKM
			location.AccuracyRadiusKM = &radius
		}
		result.Location = &location
	}
	return &result
}
