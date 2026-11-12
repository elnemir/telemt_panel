package geoip

import (
	"context"
	"errors"
	"net/url"
	"time"
)

func (m *Manager) acquireDatabase(ctx context.Context, cfg Config, item configuredDatabase, destination string) (string, string, error) {
	if cfg.Source == SourceFiles {
		location := item.config.Location
		return location, location, copyRegularFile(ctx, location, destination)
	}

	now := m.now().UTC()
	request := providerDownload(cfg, item, now)
	final, err := m.client.download(ctx, request, destination)
	if cfg.Source == SourceDBIP && errors.Is(err, errDownloadNotFound) {
		// Normalize to day 1 before going back a month (e.g. March 31).
		month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		request = providerDownload(cfg, item, month.AddDate(0, -1, 0))
		final, err = m.client.download(ctx, request, destination)
	}
	return request.url, final, err
}

func providerDownload(cfg Config, item configuredDatabase, month time.Time) downloadRequest {
	switch cfg.Source {
	case SourceMaxMind:
		edition := "GeoLite2-Country"
		switch item.kind {
		case KindCity:
			edition = "GeoLite2-City"
		case KindASN:
			edition = "GeoLite2-ASN"
		}
		return downloadRequest{
			url:       "https://download.maxmind.com/geoip/databases/" + edition + "/download?suffix=tar.gz",
			accountID: cfg.AccountID, accessKey: cfg.accessKey,
			gzip: true, archiveDB: edition + ".mmdb",
		}
	case SourceDBIP:
		return downloadRequest{
			url:  "https://download.db-ip.com/free/dbip-" + string(item.kind) + "-lite-" + month.Format("2006-01") + ".mmdb.gz",
			gzip: true,
		}
	case SourceIPinfo:
		query := url.Values{"token": {cfg.accessKey}}
		return downloadRequest{
			url:       "https://ipinfo.io/data/ipinfo_lite.mmdb?" + query.Encode(),
			accessKey: cfg.accessKey,
		}
	default:
		return downloadRequest{url: item.config.Location}
	}
}
