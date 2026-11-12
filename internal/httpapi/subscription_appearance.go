package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/subpage"
	"github.com/amirotin/telemt_panel/internal/telemt"
)

const maxSubscriptionAppearanceBody = 32 << 10

func (s *Server) handleGetSubscriptionAppearance(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := s.subAppearance.Read()
	if err != nil {
		writeSubscriptionAppearanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePutSubscriptionAppearance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request subpage.AppearanceSnapshot
	if err := decodeJSONBody(w, r, &request, jsonBodyOptions{MaxBytes: maxSubscriptionAppearanceBody, RejectUnknown: true}); err != nil || request.Revision == "" {
		writeSubscriptionAppearanceError(w, subpage.ErrSettingsInvalid)
		return
	}
	result, err := s.subAppearance.Save(request.Settings, request.Revision)
	if err != nil {
		writeSubscriptionAppearanceError(w, err)
		return
	}
	s.appendAudit(r, "subscription.appearance_change", "subscription", "")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePreviewSubscriptionAppearance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var appearance subpage.Appearance
	if err := decodeJSONBody(w, r, &appearance, jsonBodyOptions{MaxBytes: maxSubscriptionAppearanceBody, RejectUnknown: true}); err != nil {
		writeSubscriptionAppearanceError(w, subpage.ErrSettingsInvalid)
		return
	}
	appearance, err := subpage.NormalizeAppearance(appearance)
	if err != nil {
		writeSubscriptionAppearanceError(w, err)
		return
	}
	lang := r.URL.Query().Get("lang")
	if lang == "" {
		lang = r.Header.Get("Accept-Language")
	} else if lang != "ru" && lang != "en" {
		writeSubscriptionAppearanceError(w, subpage.ErrSettingsInvalid)
		return
	}
	now := time.Now().UTC()
	expiry := now.AddDate(0, 1, 0)
	const secret = "000102030405060708090a0b0c0d0e0f"
	total := uint64(64 << 20)
	quota := &telemt.QuotaEntry{UsedBytes: 16 << 20}
	u := telemt.UserInfo{Username: "demo", Enabled: true, DataQuotaBytes: &total,
		Links: telemt.UserLinks{
			TLS:     []string{"tg://proxy?server=proxy.example&port=443&secret=ee" + secret + "6578616d706c652e6f7267"},
			Secure:  []string{"tg://proxy?server=proxy.example&port=443&secret=dd" + secret},
			Classic: []string{"tg://proxy?server=proxy.example&port=443&secret=" + secret},
		}}
	switch r.URL.Query().Get("state") {
	case "", "active":
	case "disabled":
		u.Enabled = false
	case "expired":
		expiry = now.Add(-time.Hour)
	case "quota_exhausted":
		quota.UsedBytes = total
	default:
		writeSubscriptionAppearanceError(w, subpage.ErrSettingsInvalid)
		return
	}
	u.ExpirationRFC3339 = expiry.Format(time.RFC3339)
	var buf bytes.Buffer
	if err := subpage.RenderAppearance(&buf, u, quota, lang, now, appearance, s.subscriptionLogo(appearance),
		"tg://webproxy?server=web.example%2Frelay&secret=cAABAgMEBQYHCAkKCwwNDg8"); err != nil {
		writeSubscriptionAppearanceError(w, err)
		return
	}
	subscriptionHTMLHeaders(w, true)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) subscriptionLogo(appearance subpage.Appearance) []byte {
	if appearance.Logo != "panel" || s.branding == nil || s.branding.Settings().LogoStatus != "ready" {
		return nil
	}
	image, _, _ := s.branding.Asset(192)
	return image
}

func subscriptionHTMLHeaders(w http.ResponseWriter, preview bool) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	csp := "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'"
	if preview {
		csp += "; frame-ancestors 'self'; sandbox"
	} else {
		csp += "; script-src 'unsafe-inline'; frame-ancestors 'none'"
	}
	h.Set("Content-Security-Policy", csp)
}

func writeSubscriptionAppearanceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, subpage.ErrSettingsInvalid):
		auth.WriteError(w, http.StatusBadRequest, "subscription_settings_invalid", "invalid subscription appearance")
	case errors.Is(err, subpage.ErrSettingsConflict):
		auth.WriteError(w, http.StatusConflict, "subscription_settings_conflict", "subscription appearance has changed")
	default:
		slog.Error("subscription appearance", "err", err)
		auth.WriteError(w, http.StatusInternalServerError, "internal_error", "could not read, save or preview subscription appearance")
	}
}
