package branding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
)

var ErrUploadStorage = errors.New("branding_upload_storage_required")

// SaveUpload validates the raster and writes the image before publishing its
// reference. Public readers only see the snapshot after the setting is saved.
func (m *Manager) SaveUpload(title string, raw []byte) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.dataDir == "" {
		return Settings{}, ErrUploadStorage
	}
	cfg := Config{Title: title, LogoMode: "upload"}
	if err := validate(&cfg); err != nil {
		return Settings{}, err
	}
	img, _, err := decodeImage(raw)
	if err != nil {
		return Settings{}, err
	}
	// A fresh PNG strips metadata and trailing data from the supplied file.
	// 1024px is sufficient for the login screen and bounds disk and cache use.
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if longest := max(w, h); longest > 1024 {
		w, h = max(1, w*1024/longest), max(1, h*1024/longest)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, w, h))
	scaleIcon(pixels, pixels.Bounds(), img)
	var b bytes.Buffer
	if err := png.Encode(&b, pixels); err != nil {
		return Settings{}, err
	}
	s := m.empty(cfg)
	s.uploadedLogo = logoDigest(b.Bytes())
	s.setImage(b.Bytes(), pixels, "png")
	m.setURLs(s)

	path := m.uploadPath(s.uploadedLogo)
	if published, err := atomicfile.Write(path, 0600, func(w io.Writer) error {
		_, err := w.Write(s.logo)
		return err
	}); err != nil {
		if published && m.current.Load().uploadedLogo != s.uploadedLogo {
			_ = os.Remove(path)
		}
		return Settings{}, err
	}
	result, err := m.publish(s)
	if err != nil && m.current.Load().uploadedLogo != s.uploadedLogo {
		_ = os.Remove(path)
	}
	return result, err
}

func logoDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func (m *Manager) uploadPath(digest string) string {
	if m.dataDir == "" || len(digest) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return ""
	}
	return filepath.Join(m.dataDir, "branding-logo-"+digest+".png")
}

// publish is called under mu. Keep the earlier image if the store reports a
// durability error after replacement, since either record could survive a crash.
func (m *Manager) publish(s *snapshot) (Settings, error) {
	raw, _ := json.Marshal(storedConfig{Config: s.Config, UploadedLogo: s.uploadedLogo})
	err := m.st.SetSetting(settingKey, string(raw))
	if err != nil && !atomicfile.Published(err) {
		return Settings{}, err
	}
	previous := m.current.Swap(s)
	if err == nil && previous.uploadedLogo != s.uploadedLogo {
		if path := m.uploadPath(previous.uploadedLogo); path != "" {
			_ = os.Remove(path)
		}
	}
	return s.Settings, err
}
