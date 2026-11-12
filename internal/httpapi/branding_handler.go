package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/branding"
)

func (s *Server) handlePublicBranding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.branding.Public())
}

func (s *Server) handleGetBranding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.branding.Settings())
}

func (s *Server) handlePutBranding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var request struct {
		Title    *string `json:"title"`
		LogoMode *string `json:"logo_mode"`
		LogoPath *string `json:"logo_path"`
	}
	if err := decodeJSONBody(w, r, &request, jsonBodyOptions{MaxBytes: 16 << 10, RejectUnknown: true}); err != nil || request.Title == nil || request.LogoMode == nil || request.LogoPath == nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "invalid branding configuration")
		return
	}
	cfg := branding.Config{Title: *request.Title, LogoMode: *request.LogoMode, LogoPath: *request.LogoPath}
	result, err := s.branding.Save(cfg)
	s.finishBrandingSave(w, r, result, err)
}

func (s *Server) handleUploadBrandingLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	const maxBody = branding.MaxLogoBytes + (16 << 10)
	if r.ContentLength > maxBody {
		writeBrandingUploadError(w, &http.MaxBytesError{Limit: maxBody})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	form, err := r.MultipartReader()
	if err != nil {
		writeBrandingUploadError(w, err)
		return
	}
	var title *string
	var file []byte
	for {
		part, err := form.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeBrandingUploadError(w, err)
			return
		}
		switch {
		case part.FormName() == "title" && part.FileName() == "" && title == nil:
			data, err := io.ReadAll(io.LimitReader(part, 321))
			if err != nil || len(data) > 320 {
				writeBrandingUploadError(w, err)
				return
			}
			value := string(data)
			title = &value
		case part.FormName() == "file" && part.FileName() != "" && file == nil:
			file, err = io.ReadAll(io.LimitReader(part, branding.MaxLogoBytes+1))
			if err != nil || len(file) > branding.MaxLogoBytes {
				if err == nil {
					err = &http.MaxBytesError{Limit: branding.MaxLogoBytes}
				}
				writeBrandingUploadError(w, err)
				return
			}
		default:
			writeBrandingUploadError(w, nil)
			return
		}
	}
	if title == nil || len(file) == 0 {
		writeBrandingUploadError(w, nil)
		return
	}
	result, err := s.branding.SaveUpload(*title, file)
	s.finishBrandingSave(w, r, result, err)
}

func writeBrandingUploadError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		auth.WriteError(w, http.StatusRequestEntityTooLarge, "branding_logo_too_large", "logo exceeds the upload limit")
		return
	}
	auth.WriteError(w, http.StatusBadRequest, "bad_request", "invalid branding upload")
}

func (s *Server) finishBrandingSave(w http.ResponseWriter, r *http.Request, result branding.Settings, err error) {
	if err != nil {
		if errors.Is(err, branding.ErrUploadStorage) {
			auth.WriteError(w, http.StatusServiceUnavailable, err.Error(), "logo uploads require persistent panel storage")
			return
		}
		for _, known := range []error{branding.ErrTitle, branding.ErrMode, branding.ErrPath, branding.ErrRead, branding.ErrImage} {
			if errors.Is(err, known) {
				auth.WriteError(w, http.StatusBadRequest, known.Error(), "could not apply branding configuration")
				return
			}
		}
		auth.WriteError(w, http.StatusInternalServerError, "internal_error", "could not save branding configuration")
		return
	}
	s.appendAudit(r, "branding.settings_change", "panel", "")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBrandingAsset(w http.ResponseWriter, r *http.Request) {
	iconSize := 0
	if strings.HasSuffix(r.URL.Path, "/icon") {
		iconSize = 192
		if r.URL.Query().Get("size") == "512" {
			iconSize = 512
		}
	}
	body, mime, version := s.branding.Asset(iconSize)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if len(body) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	etag := `"` + version + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
