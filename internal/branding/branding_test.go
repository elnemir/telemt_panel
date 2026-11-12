package branding

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

type testStore struct {
	raw  string
	fail bool
}

func (s *testStore) GetSetting(string) (string, bool, error) { return s.raw, s.raw != "", nil }
func (s *testStore) SetSetting(_ string, raw string) error {
	if s.fail {
		return errors.New("disk full")
	}
	s.raw = raw
	return nil
}

func writeLogo(t *testing.T, path string, jpegFormat bool) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 30, 12))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	var err error
	if jpegFormat {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReloadPersistenceAndMissingFile(t *testing.T) {
	st := &testStore{}
	m := New(st, "/secret", "")
	if m.Public().Title != "Telemt Panel" || m.Public().IconURL != "" {
		t.Fatal(m.Public())
	}
	path := filepath.Join(t.TempDir(), "private-logo")
	writeLogo(t, path, false)
	cfg := Config{Title: " Private panel ", LogoMode: "custom", LogoPath: path}
	settings, err := m.Save(cfg)
	if err != nil || settings.Title != "Private panel" || settings.LogoStatus != "ready" {
		t.Fatal(settings, err)
	}
	if !strings.HasPrefix(settings.Public.LogoURL, "/secret/api/branding/logo?v=") || strings.Contains(settings.Public.LogoURL, "private-logo") {
		t.Fatal(settings.Public)
	}
	first := m.Public()
	if New(st, "/secret", "").Public() != first {
		t.Fatal("appearance did not survive restart")
	}
	icon, mime, _ := m.Asset(192)
	iconConfig, _, err := image.DecodeConfig(bytes.NewReader(icon))
	if err != nil || mime != "image/png" || iconConfig.Width != 192 || iconConfig.Height != 192 {
		t.Fatal(iconConfig, mime, err)
	}
	largeIcon, _, largeVersion := m.Asset(512)
	largeConfig, _, err := image.DecodeConfig(bytes.NewReader(largeIcon))
	if err != nil || largeConfig.Width != 512 || largeConfig.Height != 512 || !strings.HasSuffix(largeVersion, "-512") {
		t.Fatal(largeConfig, largeVersion, err)
	}
	writeLogo(t, path, true)
	if _, err := m.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if m.Public().LogoURL == first.LogoURL {
		t.Fatal("same-path replacement not reloaded")
	}
	if _, mime, _ := m.Asset(0); mime != "image/jpeg" {
		t.Fatal(mime)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := m.Asset(0); len(b) == 0 {
		t.Fatal("public request depends on live file")
	}
	restarted := New(st, "/secret", "")
	if restarted.Settings().LogoStatus != "unavailable" || restarted.Public().LogoURL != "" || restarted.Public().IconURL == "" || restarted.Public().Title != "Private panel" {
		t.Fatal(restarted.Settings())
	}
	before := m.Public()
	if _, err := m.Save(cfg); !errors.Is(err, ErrRead) || m.Public() != before {
		t.Fatal("failed read changed appearance", err)
	}
	st.fail = true
	if _, err := m.Save(Config{Title: "Changed", LogoMode: "hidden"}); err == nil || m.Public() != before {
		t.Fatal("failed persistence changed appearance")
	}
}

func TestRejectInvalidInputsAndFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logo")
	if err := os.WriteFile(path, []byte(`<svg onload="alert(1)"></svg>`), 0600); err != nil {
		t.Fatal(err)
	}
	m := New(&testStore{}, "", "")
	for _, tc := range []struct {
		cfg Config
		err error
	}{
		{Config{Title: " ", LogoMode: "hidden"}, ErrTitle},
		{Config{Title: strings.Repeat("я", 81), LogoMode: "hidden"}, ErrTitle},
		{Config{Title: "a\nb", LogoMode: "hidden"}, ErrTitle},
		{Config{Title: "a", LogoMode: "url"}, ErrMode},
		{Config{Title: "a", LogoMode: "custom", LogoPath: "https://example.org/logo"}, ErrPath},
		{Config{Title: "a", LogoMode: "custom", LogoPath: "relative.png"}, ErrPath},
		{Config{Title: "a", LogoMode: "custom", LogoPath: filepath.Dir(path)}, ErrRead},
		{Config{Title: "a", LogoMode: "custom", LogoPath: path}, ErrImage},
	} {
		if _, err := m.Save(tc.cfg); !errors.Is(err, tc.err) {
			t.Errorf("%+v: %v", tc.cfg, err)
		}
	}
	if err := os.WriteFile(path, make([]byte, (5<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(Config{Title: "a", LogoMode: "custom", LogoPath: path}); !errors.Is(err, ErrImage) {
		t.Fatal(err)
	}
	writeLogo(t, path, false)
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, body[:len(body)/2], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(Config{Title: "a", LogoMode: "custom", LogoPath: path}); !errors.Is(err, ErrImage) {
		t.Fatal("accepted truncated PNG", err)
	}
	fifo := filepath.Join(filepath.Dir(path), "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Save(Config{Title: "a", LogoMode: "custom", LogoPath: fifo}); !errors.Is(err, ErrRead) {
		t.Fatal(err)
	}
}

func TestConcurrentSnapshots(t *testing.T) {
	m := New(&testStore{}, "", "")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 20; j++ {
				_ = m.Public()
				_ = m.Settings()
				_, _, _ = m.Asset(192)
			}
		})
	}
	for j := 0; j < 20; j++ {
		if _, err := m.Save(Config{Title: "Panel", LogoMode: "hidden"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func TestUploadedLogoSurvivesRestartAndFailedReplacement(t *testing.T) {
	st := &testStore{}
	dataDir := t.TempDir()
	m := New(st, "/panel", dataDir)
	logo := filepath.Join(t.TempDir(), "image")
	writeLogo(t, logo, true)
	raw, err := os.ReadFile(logo)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := m.SaveUpload("Team", append(raw, []byte("private metadata")...))
	if err != nil || settings.LogoMode != "upload" || settings.LogoPath != "" || settings.Public.LogoMode != "custom" {
		t.Fatalf("upload settings: %+v, %v", settings, err)
	}
	body, mime, _ := m.Asset(0)
	if mime != "image/png" || bytes.Contains(body, []byte("private metadata")) {
		t.Fatalf("upload was not normalized: %q", mime)
	}
	icon := m.SubscriptionLogo()
	if len(icon) == 0 {
		t.Fatal("subscription logo missing")
	}
	restarted := New(st, "/panel", dataDir)
	if restarted.Public() != settings.Public || !bytes.Equal(restarted.SubscriptionLogo(), icon) {
		t.Fatal("branding differs after restart")
	}
	cfg := settings.Config
	cfg.Title = "New title"
	if _, err := restarted.Save(cfg); err != nil || restarted.Public().LogoURL != settings.Public.LogoURL {
		t.Fatal("renaming lost the uploaded logo", err)
	}
	writeLogo(t, logo, false)
	replacement, err := os.ReadFile(logo)
	if err != nil {
		t.Fatal(err)
	}
	st.fail = true
	if _, err := restarted.SaveUpload("Replacement", replacement); err == nil {
		t.Fatal("failed settings write was accepted")
	}
	if got := New(st, "/panel", dataDir).Public(); got.Title != cfg.Title || got.LogoURL != settings.Public.LogoURL {
		t.Fatal("failed replacement changed persisted logo", got)
	}
	st.fail = false
	if _, err := restarted.Save(Config{Title: "Team", LogoMode: "hidden"}); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dataDir)
	if err != nil || len(files) != 0 || restarted.SubscriptionLogo() != nil {
		t.Fatalf("hidden mode retained logo files or a public image: %v, %v", files, err)
	}
}
