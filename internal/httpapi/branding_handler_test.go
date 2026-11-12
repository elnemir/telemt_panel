package httpapi

import (
	"bytes"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/branding"
)

func TestBrandingAuthorizationAndValidation(t *testing.T) {
	s := newTestServer(t)
	t.Cleanup(s.geoip.Close)
	h := s.Handler()
	for _, method := range []string{"GET", "PUT"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/settings/branding", nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(method, w.Code)
		}
	}
	_, cookie := login(t, h, "admin", testPassword)
	if cookie == nil {
		t.Fatal("login failed")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/settings/branding", strings.NewReader(`{"title":"Private","logo_mode":"hidden","logo_path":""}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://untrusted.example")
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	for _, body := range []string{`null`, `{}`, `{"title":"x","logo_mode":"hidden"}`, `{"title":"x","logo_mode":"hidden","logo_path":null}`, `{"title":"x","logo_mode":"hidden","logo_path":"","extra":1}`, `{} {}`} {
		w = httptest.NewRecorder()
		r = httptest.NewRequest("PUT", "/api/settings/branding", strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("PUT", "/api/settings/branding", strings.NewReader(`{"title":"Private","logo_mode":"hidden","logo_path":""}`))
	r.AddCookie(cookie)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/branding", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "logo_path") || !strings.Contains(w.Body.String(), "Private") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/branding/icon?path=/etc/passwd", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || strings.Contains(w.Body.String(), "root:") {
		t.Fatal(w.Code, w.Header())
	}
	etag := w.Header().Get("ETag")
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "/api/branding/icon", nil)
	r.Header.Set("If-None-Match", etag)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified {
		t.Fatal(w.Code)
	}
}

func TestBrandingLogoUploadGuardsAndPublishes(t *testing.T) {
	s := newTestServer(t)
	t.Cleanup(s.geoip.Close)
	s.branding = branding.New(s.st, "", t.TempDir())
	h := s.Handler()
	_, cookie := login(t, h, "admin", testPassword)
	if cookie == nil {
		t.Fatal("login failed")
	}
	upload := func(raw []byte, anonymous, crossSite bool) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		if err := form.WriteField("title", "Team"); err != nil {
			t.Fatal(err)
		}
		file, err := form.CreateFormFile("file", "../private-name.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/settings/branding/logo", &body)
		r.Header.Set("Content-Type", form.FormDataContentType())
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if crossSite {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Origin", "https://untrusted.example")
		}
		if !anonymous {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	var raster bytes.Buffer
	if err := png.Encode(&raster, image.NewNRGBA(image.Rect(0, 0, 32, 16))); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		content              []byte
		anonymous, crossSite bool
		status               int
	}{
		{content: raster.Bytes(), anonymous: true, status: 401},
		{content: raster.Bytes(), crossSite: true, status: 403},
		{content: []byte("<svg></svg>"), status: 400},
		{content: make([]byte, branding.MaxLogoBytes+1), status: 413},
		{content: raster.Bytes(), status: 200},
	} {
		if got := upload(check.content, check.anonymous, check.crossSite); got.Code != check.status {
			t.Fatalf("want %d, got %d: %s", check.status, got.Code, got.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/branding", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-name") ||
		strings.Contains(w.Body.String(), "uploaded_logo") || !strings.Contains(w.Body.String(), "/api/branding/logo?v=") {
		t.Fatalf("public metadata: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/branding/logo", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || len(s.branding.SubscriptionLogo()) == 0 {
		t.Fatalf("public image not available: %d %v", w.Code, w.Header())
	}
}
