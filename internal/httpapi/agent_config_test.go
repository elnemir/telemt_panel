package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirotin/telemt_panel/internal/nodeagent"
)

// Run with the built Telemt executable to verify both real protocol owners.
// The proxy and real service manager are never started.
func TestAgentFullConfigProtocol(t *testing.T) {
	binary := os.Getenv("TELEMT_CONFIG_TEST_BINARY")
	if binary == "" {
		t.Skip("requires built Telemt config-file-v1 binary")
	}
	s, cookie, a, b, _, _ := nodeTestFixture(t)
	agent, _ := agentFixture(t, "test-agent-node-01")
	dir := t.TempDir()
	root := filepath.Join(dir, "telemt.toml")
	original := "[general]\nlog_level = \"normal\" # keep this comment\n[access.users]\nalice = \"0123456789abcdef0123456789abcdef\"\n"
	if err := os.WriteFile(root, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var err error
	agent.agentConfig, err = nodeagent.NewConfigFiles(binary, root, dir)
	if err != nil {
		t.Fatal(err)
	}
	as := httptest.NewServer(agent.Handler())
	defer as.Close()
	a.Binding = nodeagent.Binding{URL: as.URL, ID: agent.cfg.Agent.ID, AuthEnv: "TEST_AGENT_TOKEN"}
	if _, err := s.nodes.Replace(a.ID, a.Revision, a.Input); err != nil {
		t.Fatal(err)
	}
	request := func(method, suffix string, payload any, key string) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		headers := map[string]string{}
		if key != "" {
			headers["Idempotency-Key"] = key
		}
		return doRequest(t, s, cookie, method, nodeURL(a.ID, "/telemt/config/files"+suffix), headers, body)
	}
	w := request("GET", "", nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var snapshot nodeagent.ConfigSnapshot
	if json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Sources) != 1 {
		t.Fatal("bad manifest")
	}
	if w := doRequest(t, s, cookie, "GET", nodeURL(b.ID, "/telemt/config/files"), nil, nil); w.Code != 501 {
		t.Fatal("unbound node used another agent", w.Code)
	}
	input := map[string]any{"revision": snapshot.Revision, "source_id": snapshot.Sources[0].ID, "content": original, "edits": []any{map[string]any{"path": []string{"general", "log_level"}, "literal": `"debug"`}}}
	w = request("POST", "/render", input, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var rendered struct {
		Content string          `json:"content"`
		Tree    json.RawMessage `json:"tree"`
	}
	if json.Unmarshal(w.Body.Bytes(), &rendered) != nil || !strings.Contains(rendered.Content, "# keep this comment") || !strings.Contains(rendered.Content, `"debug"`) {
		t.Fatal("render lost source")
	}
	delete(input, "edits")
	input["content"] = rendered.Content
	w = request("POST", "/validate", input, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	disk, _ := os.ReadFile(root)
	if string(disk) != original {
		t.Fatal("validation wrote source")
	}
	key := nodeagent.NewCommandKey()
	w = request("POST", "/apply", input, key)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var committed struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(w.Body.Bytes(), &committed) != nil || committed.Revision == snapshot.Revision {
		t.Fatal("revision did not advance")
	}
	if w = request("POST", "/apply", input, key); w.Code != 200 {
		t.Fatal("replay", w.Code, w.Body)
	}
	disk, _ = os.ReadFile(root)
	if string(disk) != rendered.Content {
		t.Fatal("candidate not committed")
	}
	if w = request("POST", "/apply", input, nodeagent.NewCommandKey()); w.Code != 409 {
		t.Fatal("stale revision accepted", w.Code, w.Body)
	}
	w = request("GET", "/backups", nil, "")
	var backups []nodeagent.ConfigBackup
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &backups) != nil || len(backups) != 1 || backups[0].Source.Content != "" {
		t.Fatal("invalid backup metadata")
	}
	w = request("GET", "/backups/"+key, nil, "")
	var backup nodeagent.ConfigBackup
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &backup) != nil || backup.Source.Content != original {
		t.Fatal("backup content missing")
	}
	input["revision"] = committed.Revision
	input["content"] = backup.Source.Content
	if w = request("POST", "/apply", input, nodeagent.NewCommandKey()); w.Code != 200 {
		t.Fatal("restore failed", w.Code, w.Body)
	}
	disk, _ = os.ReadFile(root)
	if string(disk) != original {
		t.Fatal("restore did not commit original source")
	}
}
