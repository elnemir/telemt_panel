package geoip

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

type database struct {
	kind   Kind
	reader *maxminddb.Reader
	status DatabaseStatus
}

var (
	errDatabaseTooLarge     = errors.New("geoip: database exceeds size limit")
	errDatabaseTypeMismatch = errors.New("geoip: database type mismatch")
)

type bundle struct {
	databases map[Kind]*database
	dir       string
	source    Source
}

func openBundle(paths map[Kind]string, loadedAt int64) (*bundle, error) {
	return openBundleWithVerifier(paths, loadedAt, (*maxminddb.Reader).Verify)
}

func openBundleWithVerifier(paths map[Kind]string, loadedAt int64, verify func(*maxminddb.Reader) error) (*bundle, error) {
	out := &bundle{databases: make(map[Kind]*database, len(paths))}
	for _, kind := range []Kind{KindCountry, KindASN, KindCity} {
		path, ok := paths[kind]
		if !ok {
			continue
		}
		db, err := openDatabase(kind, path, loadedAt, verify)
		if err != nil {
			out.close()
			return nil, err
		}
		out.databases[kind] = db
	}
	if len(out.databases) == 0 {
		return nil, errors.New("geoip: bundle contains no databases")
	}
	return out, nil
}

func openDatabase(kind Kind, path string, loadedAt int64, verify func(*maxminddb.Reader) error) (*database, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: inspect database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("geoip: database is not a regular file")
	}
	if info.Size() > maxDatabaseBytes {
		return nil, errDatabaseTooLarge
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: open database: %w", err)
	}
	if err := verify(reader); err != nil {
		reader.Close()
		return nil, fmt.Errorf("geoip: verify database: %w", err)
	}
	if !databaseTypeMatches(kind, reader.Metadata.DatabaseType) {
		reader.Close()
		return nil, fmt.Errorf("%w: %s", errDatabaseTypeMismatch, kind)
	}
	if !validBuildEpoch(reader.Metadata.BuildEpoch, time.Now().Unix()) {
		reader.Close()
		return nil, errors.New("geoip: database build epoch is invalid")
	}
	if err := verifyRecordSchema(reader, kind); err != nil {
		reader.Close()
		return nil, fmt.Errorf("geoip: invalid database record: %w", err)
	}
	return &database{
		kind: kind, reader: reader,
		status: DatabaseStatus{Kind: kind, BuildEpochSecs: int64(reader.Metadata.BuildEpoch), LoadedEpochSecs: loadedAt},
	}, nil
}

func verifyRecordSchema(reader *maxminddb.Reader, kind Kind) error {
	// Verify checks the MMDB encoding. Networks can share a data record;
	// its offset identifies identical decoded fields within this reader.
	// Check every distinct record, not the same record once per IP range.
	seen := make(map[uintptr]struct{})
	lite := isIPinfoLite(reader.Metadata.DatabaseType)
	for result := range reader.Networks() {
		if err := result.Err(); err != nil {
			return err
		}
		offset := result.Offset()
		if _, ok := seen[offset]; ok {
			continue
		}
		var record any
		switch {
		case lite:
			record = &ipinfoLiteRecord{}
		case kind == KindCountry:
			record = &countryRecord{}
		case kind == KindASN:
			record = &asnRecord{}
		case kind == KindCity:
			record = &cityRecord{}
		}
		if err := result.Decode(record); err != nil {
			return err
		}
		if city, ok := record.(*cityRecord); ok {
			if _, err := city.location(); err != nil {
				return err
			}
		}
		if item, ok := record.(*ipinfoLiteRecord); ok {
			if _, err := item.asn(); err != nil {
				return err
			}
		}
		seen[offset] = struct{}{}
	}
	return nil
}

func validBuildEpoch(epoch uint, now int64) bool {
	return epoch > 0 && uint64(epoch) <= uint64(now+int64(48*time.Hour/time.Second))
}

func databaseTypeMatches(kind Kind, databaseType string) bool {
	if isIPinfoLite(databaseType) {
		return kind == KindCountry || kind == KindASN
	}
	name := strings.ToLower(databaseType)
	switch kind {
	case KindCountry:
		return strings.Contains(name, "country") && !strings.Contains(name, "city")
	case KindASN:
		return strings.Contains(name, "asn")
	case KindCity:
		return strings.Contains(name, "city")
	default:
		return false
	}
}

func isIPinfoLite(databaseType string) bool {
	return databaseType == "ipinfo bundle_location_lite.mmdb"
}

type countryRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
}

type cityRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	City struct {
		Names map[string]string `maxminddb:"names"`
		ID    *uint32           `maxminddb:"geoname_id"`
	} `maxminddb:"city"`
	Location struct {
		Latitude       *float64 `maxminddb:"latitude"`
		Longitude      *float64 `maxminddb:"longitude"`
		AccuracyRadius *uint16  `maxminddb:"accuracy_radius"`
	} `maxminddb:"location"`
}

func (r cityRecord) location() (*Location, error) {
	for _, coordinate := range []struct {
		value *float64
		bound float64
	}{
		{r.Location.Latitude, 90}, {r.Location.Longitude, 180},
	} {
		if coordinate.value != nil && (math.IsNaN(*coordinate.value) || math.IsInf(*coordinate.value, 0) || math.Abs(*coordinate.value) > coordinate.bound) {
			return nil, errors.New("geoip: invalid City coordinates")
		}
	}
	if r.Location.Latitude == nil || r.Location.Longitude == nil {
		return nil, nil
	}
	return &Location{Latitude: *r.Location.Latitude, Longitude: *r.Location.Longitude, AccuracyRadiusKM: r.Location.AccuracyRadius}, nil
}

func applyCity(out *Result, record cityRecord) error {
	location, err := record.location()
	if err != nil {
		return err
	}
	if out.CountryCode != "" && record.Country.ISOCode != "" && out.CountryCode != record.Country.ISOCode {
		out.CountryConflict = true
		return nil
	}
	if out.CountryCode == "" {
		applyCountry(out, record.Country.ISOCode, record.Country.Names)
	}
	out.City, out.CityRU = record.City.Names["en"], record.City.Names["ru"]
	if record.City.ID != nil && *record.City.ID > 0 {
		out.CityID = record.City.ID
	}
	out.Location = location
	return nil
}

type asnRecord struct {
	Number       uint   `maxminddb:"autonomous_system_number"`
	Organization string `maxminddb:"autonomous_system_organization"`
}

type ipinfoLiteRecord struct {
	CountryCode string `maxminddb:"country_code"`
	CountryName string `maxminddb:"country"`
	ASN         string `maxminddb:"asn"`
	ASName      string `maxminddb:"as_name"`
}

func (r ipinfoLiteRecord) asn() (uint, error) {
	if r.ASN == "" {
		return 0, nil
	}
	value, found := strings.CutPrefix(r.ASN, "AS")
	n, err := strconv.ParseUint(value, 10, 32)
	if !found || err != nil {
		return 0, errors.New("geoip: invalid IPinfo ASN")
	}
	return uint(n), nil
}

func (db *database) applyCountry(out *Result, addr netip.Addr) error {
	result := db.reader.Lookup(addr)
	if isIPinfoLite(db.reader.Metadata.DatabaseType) {
		var record ipinfoLiteRecord
		if err := result.Decode(&record); err != nil {
			return err
		}
		out.CountryCode, out.CountryName = record.CountryCode, record.CountryName
		return nil
	}
	var record countryRecord
	if err := result.Decode(&record); err != nil {
		return err
	}
	applyCountry(out, record.Country.ISOCode, record.Country.Names)
	return nil
}

func (db *database) applyASN(out *Result, addr netip.Addr) error {
	result := db.reader.Lookup(addr)
	if isIPinfoLite(db.reader.Metadata.DatabaseType) {
		var record ipinfoLiteRecord
		if err := result.Decode(&record); err != nil {
			return err
		}
		asn, err := record.asn()
		if err != nil {
			return err
		}
		out.ASN, out.Organization = asn, record.ASName
		return nil
	}
	var record asnRecord
	if err := result.Decode(&record); err != nil {
		return err
	}
	out.ASN, out.Organization = record.Number, record.Organization
	return nil
}

func (b *bundle) lookup(addr netip.Addr) (Result, error) {
	addr = addr.Unmap()
	out := Result{State: ResultNotFound}
	if db := b.databases[KindCountry]; db != nil {
		if err := db.applyCountry(&out, addr); err != nil {
			return Result{}, err
		}
	}
	if db := b.databases[KindCity]; db != nil {
		var record cityRecord
		if err := db.reader.Lookup(addr).Decode(&record); err != nil {
			return Result{}, err
		}
		if err := applyCity(&out, record); err != nil {
			return Result{}, err
		}
	}
	if db := b.databases[KindASN]; db != nil {
		if err := db.applyASN(&out, addr); err != nil {
			return Result{}, err
		}
	}
	if out.CountryCode != "" || out.CountryName != "" || out.CountryNameRU != "" ||
		out.City != "" || out.CityRU != "" || out.CityID != nil || out.Location != nil || out.ASN != 0 || out.Organization != "" {
		out.State = ResultFound
	}
	return out, nil
}

func applyCountry(out *Result, code string, names map[string]string) {
	out.CountryCode = code
	out.CountryName = names["en"]
	out.CountryNameRU = names["ru"]
}

func (b *bundle) statuses() []DatabaseStatus {
	out := make([]DatabaseStatus, 0, len(b.databases))
	for _, kind := range []Kind{KindCountry, KindASN, KindCity} {
		if db := b.databases[kind]; db != nil {
			out = append(out, db.status)
		}
	}
	return out
}

func (b *bundle) close() {
	for _, db := range b.databases {
		_ = db.reader.Close()
	}
	b.databases = nil
}
