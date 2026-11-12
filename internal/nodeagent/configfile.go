package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
)

const MaxConfigRequest = 16 * 1024 * 1024
const MaxConfigResponse = 64 * 1024 * 1024
const MaxConfigBackups = 32

var ErrConfigTool = errors.New("configuration tool unavailable or returned an invalid response")
var ErrConfigBackup = errors.New("configuration backup unavailable or capacity reached")
var sourcePattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ConfigEdit struct {
	Path    []json.RawMessage `json:"path"`
	Literal *string           `json:"literal"`
}
type ConfigRequest struct {
	Edits    []ConfigEdit `json:"edits,omitempty"`
	Action   string       `json:"action"`
	Revision string       `json:"revision,omitempty"`
	SourceID string       `json:"source_id,omitempty"`
	Content  string       `json:"content,omitempty"`
}
type ConfigSource struct {
	Tree    json.RawMessage `json:"tree,omitempty"`
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Content string          `json:"content"`
}
type ConfigSnapshot struct {
	Protocol int            `json:"protocol"`
	Revision string         `json:"revision"`
	Sources  []ConfigSource `json:"sources"`
}
type ConfigResult struct {
	OK     bool            `json:"ok"`
	Status int             `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  struct {
		Code string `json:"code"`
	} `json:"error,omitempty"`
}
type ConfigBackup struct {
	ID       string       `json:"id"`
	Created  int64        `json:"created"`
	Revision string       `json:"revision"`
	Source   ConfigSource `json:"source"`
}
type configExecutor func(context.Context, string, []string, []byte, int, bool) ([]byte, error)
type ConfigFiles struct {
	mu                    sync.Mutex
	binary, root, backups string
	exec                  configExecutor
}

func NewConfigFiles(binary, root, dataDir string) (*ConfigFiles, error) {
	if !filepath.IsAbs(binary) || !filepath.IsAbs(root) || dataDir == "" {
		return nil, ErrConfigTool
	}
	dir := filepath.Join(dataDir, "config-backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, ErrConfigBackup
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrConfigBackup
	}
	return &ConfigFiles{binary: binary, root: root, backups: dir, exec: executeConfigProcess}, nil
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		return 0, ErrConfigTool
	}
	return w.Buffer.Write(p)
}
func executeConfigProcess(ctx context.Context, binary string, args []string, input []byte, limit int, help bool) ([]byte, error) {
	out := &limitedOutput{limit: limit}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	// Help in supported Telemt versions is printed to stderr. Ordinary command
	// diagnostics never reach the panel or logs; only the JSON protocol does.
	if help {
		cmd.Stderr = out
	} else {
		cmd.Stderr = io.Discard
	}
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, ErrConfigTool
	}
	if ctx.Err() != nil {
		return nil, ErrConfigTool
	}
	return out.Bytes(), nil
}
func (c *ConfigFiles) call(ctx context.Context, request ConfigRequest) (ConfigResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	help, err := c.exec(ctx, c.binary, []string{"--help"}, nil, 128*1024, true)
	if err != nil || !bytes.Contains(help, []byte("config-file-v1 JSON protocol")) {
		return ConfigResult{}, ErrConfigTool
	}
	body, err := json.Marshal(request)
	if err != nil || len(body) > MaxConfigRequest {
		return ConfigResult{}, ErrConfigTool
	}
	raw, err := c.exec(ctx, c.binary, []string{"config-file", c.root}, body, MaxConfigResponse, false)
	if err != nil {
		return ConfigResult{}, ErrConfigTool
	}
	var result ConfigResult
	if json.Unmarshal(raw, &result) != nil || result.Status < 200 || result.Status > 599 || result.OK != (result.Status == 200) {
		return ConfigResult{}, ErrConfigTool
	}
	if result.OK {
		var version struct {
			Protocol int `json:"protocol"`
		}
		if json.Unmarshal(result.Data, &version) != nil || version.Protocol != 1 {
			return ConfigResult{}, ErrConfigTool
		}
	}
	return result, nil
}
func (c *ConfigFiles) Inspect(ctx context.Context) (ConfigResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.call(ctx, ConfigRequest{Action: "inspect"})
}
func (c *ConfigFiles) Validate(ctx context.Context, request ConfigRequest) (ConfigResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	request.Action = "validate"
	return c.call(ctx, request)
}

func (c *ConfigFiles) Render(ctx context.Context, request ConfigRequest) (ConfigResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	request.Action = "render"
	return c.call(ctx, request)
}

// Apply saves the exact old owner before invoking the CLI's compare-and-replace.
// The CLI checks the graph again under the same advisory lock as all API writes.
// A failed backup prevents mutation. Backup presence alone never proves commit.
func (c *ConfigFiles) Apply(ctx context.Context, key string, request ConfigRequest) (ConfigResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !ValidCommandKeySyntax(key) || !sourcePattern.MatchString(request.SourceID) {
		return ConfigResult{}, ErrConfigTool
	}
	current, err := c.call(ctx, ConfigRequest{Action: "inspect"})
	if err != nil || !current.OK {
		return current, err
	}
	var snapshot ConfigSnapshot
	if json.Unmarshal(current.Data, &snapshot) != nil {
		return ConfigResult{}, ErrConfigTool
	}
	if snapshot.Revision != request.Revision {
		return configFailure(409, "revision_conflict"), nil
	}
	var source *ConfigSource
	for i := range snapshot.Sources {
		if snapshot.Sources[i].ID == request.SourceID {
			source = &snapshot.Sources[i]
			break
		}
	}
	if source == nil {
		return configFailure(404, "config_source_not_found"), nil
	}
	check := request
	check.Action = "validate"
	checked, err := c.call(ctx, check)
	if err != nil || !checked.OK {
		return checked, err
	}
	entries, err := os.ReadDir(c.backups)
	if err != nil || len(entries) >= MaxConfigBackups {
		return ConfigResult{}, ErrConfigBackup
	}
	destination := filepath.Join(c.backups, key+".json")
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return ConfigResult{}, ErrConfigBackup
	}
	backup := ConfigBackup{ID: key, Created: time.Now().Unix(), Revision: snapshot.Revision, Source: *source}
	backup.Source.Tree = nil
	encoded, err := json.Marshal(backup)
	if err != nil || len(encoded) > MaxConfigRequest {
		return ConfigResult{}, ErrConfigBackup
	}
	_, err = atomicfile.Write(destination, 0600, func(w io.Writer) error { _, err := w.Write(encoded); return err })
	if err != nil {
		return ConfigResult{}, ErrConfigBackup
	}
	request.Action = "apply"
	return c.call(ctx, request)
}
func configFailure(status int, code string) ConfigResult {
	r := ConfigResult{Status: status}
	r.Error.Code = code
	return r
}

func (c *ConfigFiles) Backups() ([]ConfigBackup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.backups)
	if err != nil || len(entries) > MaxConfigBackups {
		return nil, ErrConfigBackup
	}
	backups := make([]ConfigBackup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		key := entry.Name()[:len(entry.Name())-5]
		backup, err := c.readBackup(key)
		if err != nil {
			return nil, err
		}
		backup.Source.Content = "" // List metadata only; contents require explicit retrieval.
		backups = append(backups, backup)
	}
	return backups, nil
}
func (c *ConfigFiles) Backup(key string) (ConfigBackup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readBackup(key)
}
func (c *ConfigFiles) readBackup(key string) (ConfigBackup, error) {
	if !ValidCommandKeySyntax(key) {
		return ConfigBackup{}, ErrConfigBackup
	}
	path := filepath.Join(c.backups, key+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxConfigRequest {
		return ConfigBackup{}, ErrConfigBackup
	}
	f, err := os.Open(path)
	if err != nil {
		return ConfigBackup{}, ErrConfigBackup
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxConfigRequest+1))
	if err != nil || len(raw) > MaxConfigRequest {
		return ConfigBackup{}, ErrConfigBackup
	}
	var backup ConfigBackup
	if json.Unmarshal(raw, &backup) != nil || backup.ID != key || !sourcePattern.MatchString(backup.Source.ID) {
		return ConfigBackup{}, ErrConfigBackup
	}
	return backup, nil
}

// DeleteBackup removes one explicitly selected backup, never a live source.
func (c *ConfigFiles) DeleteBackup(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.readBackup(key); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(c.backups, key+".json")); err != nil {
		return ErrConfigBackup
	}
	dir, err := os.Open(c.backups)
	if err != nil {
		return ErrConfigBackup
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrConfigBackup
	}
	return nil
}
