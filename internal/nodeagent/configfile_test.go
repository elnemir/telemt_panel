package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configFixture(t *testing.T) (*ConfigFiles, ConfigRequest, *int) {
	t.Helper()
	c, e := NewConfigFiles("/installed/telemt", "/srv/telemt.toml", t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	request := ConfigRequest{Revision: strings.Repeat("a", 64), SourceID: strings.Repeat("b", 64), Content: "[general]\nlog_level='debug'\n"}
	applied := 0
	c.exec = func(_ context.Context, _ string, args []string, input []byte, _ int, help bool) ([]byte, error) {
		if help {
			return []byte("config-file-v1 JSON protocol"), nil
		}
		if len(args) != 2 || args[0] != "config-file" || args[1] != "/srv/telemt.toml" {
			t.Fatal(args)
		}
		var r ConfigRequest
		if json.Unmarshal(input, &r) != nil {
			t.Fatal("invalid protocol request")
		}
		data := any(map[string]any{"protocol": 1, "revision": request.Revision, "valid": true})
		switch r.Action {
		case "inspect":
			data = ConfigSnapshot{Protocol: 1, Revision: request.Revision, Sources: []ConfigSource{{ID: request.SourceID, Name: "/srv/telemt.toml", Content: "original secret-bearing source"}}}
		case "validate":
			if r.Content == "invalid" {
				return []byte(`{"ok":false,"status":400,"error":{"code":"config_validation_failed"}}`), nil
			}
		case "apply":
			entries, e := os.ReadDir(c.backups)
			if e != nil || len(entries) != 1 {
				t.Fatal("apply without backup", entries, e)
			}
			raw, e := os.ReadFile(filepath.Join(c.backups, entries[0].Name()))
			if e != nil || !strings.Contains(string(raw), "original secret-bearing source") {
				t.Fatal("wrong backup", e)
			}
			applied++
			data = map[string]any{"protocol": 1, "revision": strings.Repeat("c", 64), "persisted": true}
		default:
			t.Fatal(r.Action)
		}
		raw, _ := json.Marshal(map[string]any{"ok": true, "status": 200, "data": data})
		return raw, nil
	}
	return c, request, &applied
}
func TestConfigFilesBackupBeforeCommitAndExplicitRecovery(t *testing.T) {
	c, r, applied := configFixture(t)
	key := NewCommandKey()
	result, e := c.Apply(context.Background(), key, r)
	if e != nil || !result.OK || *applied != 1 {
		t.Fatal(result, e, *applied)
	}
	listed, e := c.Backups()
	if e != nil || len(listed) != 1 || listed[0].Source.Content != "" {
		t.Fatal(listed, e)
	}
	saved, e := c.Backup(key)
	if e != nil || saved.Source.Content != "original secret-bearing source" || saved.Revision != r.Revision {
		t.Fatal(saved, e)
	}
	info, e := os.Stat(filepath.Join(c.backups, key+".json"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	if c.DeleteBackup("../../telemt.toml") == nil {
		t.Fatal("path escaped backup registry")
	}
	if e := c.DeleteBackup(key); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Backup(key); e == nil {
		t.Fatal("backup survived explicit deletion")
	}
}
func TestConfigFilesRejectsInvalidStaleAndMissingBackupCapacity(t *testing.T) {
	c, r, applied := configFixture(t)
	stale := r
	stale.Revision = strings.Repeat("f", 64)
	res, e := c.Apply(context.Background(), NewCommandKey(), stale)
	if e != nil || res.Status != 409 || *applied != 0 {
		t.Fatal(res, e)
	}
	invalid := r
	invalid.Content = "invalid"
	res, e = c.Apply(context.Background(), NewCommandKey(), invalid)
	if e != nil || res.Status != 400 || *applied != 0 {
		t.Fatal(res, e)
	}
	for i := range MaxConfigBackups {
		if e := os.WriteFile(filepath.Join(c.backups, string(rune('A'+i))), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := c.Apply(context.Background(), NewCommandKey(), r); e == nil || *applied != 0 {
		t.Fatal("capacity did not block write", e)
	}
}
func TestConfigFilesNeverInvokesUnknownSubcommand(t *testing.T) {
	c, _, _ := configFixture(t)
	calls := 0
	c.exec = func(_ context.Context, _ string, args []string, _ []byte, _ int, help bool) ([]byte, error) {
		calls++
		if !help || len(args) != 1 || args[0] != "--help" {
			t.Fatal("old binary would run proxy", args)
		}
		return []byte("legacy help"), nil
	}
	if _, e := c.Inspect(context.Background()); e == nil || calls != 1 {
		t.Fatal(e, calls)
	}
}
