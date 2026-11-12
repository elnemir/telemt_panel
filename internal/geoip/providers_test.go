package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMaxMindDownloadsArchiveAndKeepsKeyAcrossRestart(t *testing.T) {
	dataDir, store := t.TempDir(), newMemorySettings()
	fixture, err := os.ReadFile(writeFixture(t, "country"))
	if err != nil {
		t.Fatal(err)
	}
	archive := geoIPArchive(t, "GeoLite2-Country_20261101/GeoLite2-Country.mmdb", fixture)
	client := newSecureDownloader().(*secureDownloader)
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "download.maxmind.com" {
			id, key, ok := req.BasicAuth()
			if key == "rejected-key" {
				return providerResponse(req, http.StatusUnauthorized, nil), nil
			}
			if !ok || id != "123" || key != "fixture-key" || req.URL.Path != "/geoip/databases/GeoLite2-Country/download" || req.URL.Query().Get("suffix") != "tar.gz" {
				t.Error("invalid MaxMind request")
			}
			resp := providerResponse(req, http.StatusFound, nil)
			resp.Header.Set("Location", "https://files.download.maxmind.com/file.tar.gz?signed=temporary")
			return resp, nil
		}
		if req.Header.Get("Authorization") != "" || req.Header.Get("Referer") != "" {
			t.Error("redirect forwarded credential-bearing headers")
		}
		return providerResponse(req, http.StatusOK, archive), nil
	})
	m := NewManager(dataDir, store)
	defer m.Close()
	m.client = client
	cfg := Config{Enabled: true, Source: SourceMaxMind, AccountID: "123", Schedule: ScheduleManual, Country: DatabaseConfig{Enabled: true}}
	key := "fixture-key"
	if _, err := m.PutConfigWithKey(cfg, &key); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StateReady)
	response, err := json.Marshal(m.Settings())
	if err != nil || !m.Settings().AccessKeySaved || strings.Contains(string(response), key) || strings.Contains(string(response), "temporary") {
		t.Fatal("settings did not keep credential and redirect query private")
	}
	m.Close()

	restored := NewManager(dataDir, store)
	defer restored.Close()
	restored.client = client
	if settings := restored.Settings(); !settings.AccessKeySaved || settings.Status.State != StateReady {
		t.Fatal("key and active provider did not survive restart")
	}
	if _, err := restored.Update(); err != nil {
		t.Fatal(err)
	}
	waitState(t, restored, StateReady)
	rejected := "rejected-key"
	if _, err := restored.PutConfigWithKey(cfg, &rejected); err != nil {
		t.Fatal(err)
	}
	status := waitState(t, restored, StateError)
	if !status.Available || status.LastError == nil || *status.LastError != ErrorProviderAccess {
		t.Fatal("rejected key did not retain the active bundle and report access denied")
	}
	cfg.AccountID = "456"
	if _, err := restored.PutConfig(cfg); err == nil {
		t.Fatal("changing account ID must require a new key")
	}
}

func TestDBIPFallsBackToPreviousMonthOnNotFound(t *testing.T) {
	fixture, err := os.ReadFile(writeFixture(t, "country"))
	if err != nil {
		t.Fatal(err)
	}
	archive := geoIPArchive(t, "", fixture)
	client := newSecureDownloader().(*secureDownloader)
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "download.db-ip.com" || req.Header.Get("Authorization") != "" {
			t.Error("invalid DB-IP request")
		}
		switch req.URL.Path {
		case "/free/dbip-country-lite-2026-03.mmdb.gz":
			return providerResponse(req, http.StatusNotFound, nil), nil
		case "/free/dbip-country-lite-2026-02.mmdb.gz":
			return providerResponse(req, http.StatusOK, archive), nil
		default:
			t.Error("invalid DB-IP month")
			return providerResponse(req, http.StatusBadRequest, nil), nil
		}
	})
	m := NewManager(t.TempDir(), newMemorySettings())
	defer m.Close()
	m.client = client
	now := time.Date(2026, time.March, 31, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	cfg := Config{Enabled: true, Source: SourceDBIP, Schedule: ScheduleDaily, Country: DatabaseConfig{Enabled: true}}
	if _, err := m.PutConfig(cfg); err != nil {
		t.Fatal(err)
	}
	status := waitState(t, m, StateReady)
	if p := status.Databases[0].Provenance; !strings.HasSuffix(p.FinalLocation, "2026-02.mmdb.gz") {
		t.Fatalf("unexpected DB-IP release: %s", p.FinalLocation)
	}
	now = now.Add(24 * time.Hour)
	if !m.refreshDue() {
		t.Fatal("scheduled provider refresh not due")
	}
}

func TestIPinfoDownloadsCombinedDatabaseOnce(t *testing.T) {
	// Synthetic IPinfo Lite MMDB. See FIXTURES.md.
	const fixture = "H4sIAAAAAAAA/2TPV1MTXRwG8OcNeXuzV9TYO6ICdoUgRggBFLAhrpvdk3Bg92zc3UCCvffezY1c8yn8AF74EZzxxiu/gkOeTM6gzDC/8zz/UzbALwA2AREAfwM1AEpAlPwK4A/gN6bfSYn8qdNf1fIfAMvLK/wL/Mct/5MZ+sBMYBbTbDKHzCXzyHx9YAGwkGmRLhcDtUxLyFI9WwbEdJoEVgBYB6zU5SpgNdMaslbPKqwnG8jG6mwzgJbyCnXAFn2gHtgKYA+wTZfbSQNpJE1kB9lJdpHd1Yf28pZ9ZD85QJr1CyUgztRKDgK4A7QB8IFDLBPkMIARoB3AINDBMkk6SYp0kW7SQ46Qo6SX9JF+cgzAaeA40wlykpwiA9M+vsIZnQxyFjBZpolFbL1TkAyQZTlEpN4yXH3B0aULKJ08IMd0TpcVAiBkmSejZAzA+fIKBaDIcvyn4xeAiywv6fIyuQJc1eU1ch24wfKmnt0CbutUAu7qdI/cBx6wfEgekcfkCXlKnpHn5IW+7CV5RV4Dbzh7S97pnVP/0/6+JMzAUKYrutsKpptzRKxLhGOeP9JqBirR0tvU0Fhfn7C8vAr9Yle/kqGwY0mpsrbndlZqw/JsEU/EJz9+S5mFlFR2neW5XwfSUpl+0ch4vmuGhmsOe74xKvxAemoi8uNUKj19n0znpWMbIudZQ9FI5pNsTtlmaKbNQBhhMScGa2ROqowXS+eV7QjD8SwzlJ4yHBmKOte100lbBJYvc1Pt57hQg5HeogqHRCitWJ9whBvG2nvKV2RkIcz7okPmqh8XbXdMlc2bWREg2qE8Wxjln/qhlPSF5fm2EchxMVH7fQB+ZnoHigUAAA=="
	decoded := base64.NewDecoder(base64.StdEncoding, strings.NewReader(fixture))
	gz, err := gzip.NewReader(decoded)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(gz)
	_ = gz.Close()
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	client := newSecureDownloader().(*secureDownloader)
	key := "fixture-token"
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.URL.Host != "ipinfo.io" || req.URL.Path != "/data/ipinfo_lite.mmdb" || req.URL.Query().Get("token") != key {
			t.Error("invalid IPinfo download request")
		}
		return providerResponse(req, http.StatusOK, data), nil
	})
	m := NewManager(t.TempDir(), newMemorySettings())
	defer m.Close()
	m.client = client
	cfg := Config{Enabled: true, Source: SourceIPinfo, Schedule: ScheduleManual, Country: DatabaseConfig{Enabled: true}, ASN: DatabaseConfig{Enabled: true}}
	if _, err := m.PutConfigWithKey(cfg, &key); err != nil {
		t.Fatal(err)
	}
	status := waitState(t, m, StateReady)
	result := m.Lookup("81.2.69.142")
	if requests != 1 || len(status.Databases) != 2 || result == nil || result.CountryCode != "GB" || result.ASN != 64500 || result.Organization != "Example Network" {
		t.Fatalf("IPinfo did not activate Country and ASN from one download: requests=%d result=%+v", requests, result)
	}
	for _, db := range status.Databases {
		if db.Provenance == nil || strings.Contains(db.Provenance.OriginalLocation, key) || strings.Contains(db.Provenance.FinalLocation, key) {
			t.Fatal("IPinfo token leaked or provenance is missing")
		}
	}
}

func providerResponse(req *http.Request, code int, data []byte) *http.Response {
	return &http.Response{Request: req, StatusCode: code, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}
}

func geoIPArchive(t *testing.T, entry string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if entry == "" {
		if _, err := gz.Write(data); err != nil {
			t.Fatal(err)
		}
	} else {
		archive := tar.NewWriter(gz)
		if err := archive.WriteHeader(&tar.Header{Name: entry, Mode: 0o600, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExpandedDownloadIsBounded(t *testing.T) {
	archive := geoIPArchive(t, "", bytes.Repeat([]byte("a"), 1024))
	err := writeDownload(context.Background(), io.Discard, bytes.NewReader(archive), downloadRequest{gzip: true}, 256)
	if errorCode(err) != ErrorDownloadTooLarge {
		t.Fatalf("unexpected oversized gzip error: %v", err)
	}
}
