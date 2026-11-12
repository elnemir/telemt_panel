package subpage

import (
	"encoding/base64"
	"io"
	"slices"
	"time"

	"github.com/amirotin/telemt_panel/internal/telemt"
)

type appearanceView struct {
	Appearance
	Copy    PageCopy
	LogoURI string
}

func (d pageData) shows(block string) bool {
	return d.Appearance == nil || !slices.Contains(d.Appearance.Hidden, block)
}

// RenderAppearance uses the same page model for live users and preview data.
func RenderAppearance(w io.Writer, u telemt.UserInfo, quota *telemt.QuotaEntry, acceptLanguage string, now time.Time, appearance Appearance, panelLogoPNG []byte, webLinks ...string) error {
	lang := appearance.Language
	if lang == "auto" {
		lang = detectLanguage(acceptLanguage)
	}
	links := u.Links
	if !slices.Contains(appearance.Connections, "tls") {
		links.TLS, links.TLSDomains = nil, nil
	}
	if !slices.Contains(appearance.Connections, "secure") {
		links.Secure = nil
	}
	if !slices.Contains(appearance.Connections, "classic") {
		links.Classic = nil
	}
	if !slices.Contains(appearance.Connections, "web") {
		webLinks = nil
	}
	u.Links = links
	data, err := buildPageData(u, quota, lang, now, webLinks...)
	if err != nil {
		return err
	}
	view := &appearanceView{Appearance: appearance, Copy: appearance.EN}
	if lang == "ru" {
		view.Copy = appearance.RU
	}
	if appearance.Logo == "panel" && len(panelLogoPNG) != 0 {
		view.LogoURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(panelLogoPNG)
	}
	if view.Copy.Title != "" {
		data.S.Title = view.Copy.Title
	}
	// Status is computed before hiding the optional cards.
	if slices.Contains(appearance.Hidden, "quota") {
		data.Quota = nil
	}
	if slices.Contains(appearance.Hidden, "expiry") {
		data.Expiry = nil
	}
	data.Appearance = view
	return renderPageData(w, data)
}
