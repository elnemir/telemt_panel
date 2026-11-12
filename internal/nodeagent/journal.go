// Package nodeagent provides the narrow authenticated node host transport.
package nodeagent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
)

const journalKey = "agent.commands.v1"
const MaxCommands = 2048
const CommandLifetime = 24 * time.Hour

var commandPattern = regexp.MustCompile(`^[0-9]{10,12}-[0-9a-f]{32}$`)
var ErrJournal = errors.New("agent command journal unavailable")
var ErrKey = errors.New("agent command key invalid or expired")
var ErrConflict = errors.New("agent command key reused with another action")
var ErrUnknown = errors.New("agent command result unknown; inspect service before issuing a new command")
var ErrCapacity = errors.New("agent command journal capacity reached")

type State interface {
	GetSetting(string) (string, bool, error)
	SetSetting(string, string) error
	StateDurable() bool
}
type CommandResult struct {
	Key     string          `json:"key"`
	Action  string          `json:"action"`
	Started int64           `json:"started"`
	Status  int             `json:"status"`
	Body    json.RawMessage `json:"body,omitempty"`
}
type Journal struct {
	mu      sync.Mutex
	state   State
	records map[string]CommandResult
	now     func() time.Time
}

func ValidCommandKeySyntax(key string) bool { return commandPattern.MatchString(key) }

func NewCommandKey() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(id[:]))
}

func OpenJournal(state State) (*Journal, error) {
	if !state.StateDurable() {
		return nil, ErrJournal
	}
	j := &Journal{state: state, records: map[string]CommandResult{}, now: time.Now}
	raw, found, err := state.GetSetting(journalKey)
	if err != nil {
		return nil, ErrJournal
	}
	if found {
		if len(raw) > 2*1024*1024 || json.Unmarshal([]byte(raw), &j.records) != nil || j.records == nil || len(j.records) > MaxCommands {
			return nil, ErrJournal
		}
	}
	for key, r := range j.records {
		if key != r.Key || !commandPattern.MatchString(key) || r.Action == "" || r.Started <= 0 || (r.Status != 0 && (r.Status < 200 || r.Status > 599)) || len(r.Body) > 16*1024 {
			return nil, ErrJournal
		}
	}
	return j, nil
}

func (j *Journal) validKey(key string) bool {
	if !commandPattern.MatchString(key) {
		return false
	}
	epoch, err := strconv.ParseInt(strings.SplitN(key, "-", 2)[0], 10, 64)
	age := j.now().Sub(time.Unix(epoch, 0))
	return err == nil && age >= -time.Minute && age <= CommandLifetime
}

func (j *Journal) save(records map[string]CommandResult) error {
	raw, err := json.Marshal(records)
	if err != nil || len(raw) > 2*1024*1024 {
		return ErrJournal
	}
	err = j.state.SetSetting(journalKey, string(raw))
	// Visible but not durable is still an intent: never repeat its side effect.
	if err == nil || atomicfile.Published(err) {
		j.records = records
	}
	if err != nil {
		return ErrJournal
	}
	return nil
}

// Execute serializes host mutations and durably records intent BEFORE calling
// the allowlisted action. A crash with an unfinished intent is never retried.
// The key expires before records are pruned, so pruning cannot permit replay.
func (j *Journal) Execute(key, action string, run func() (int, []byte)) (CommandResult, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.validKey(key) {
		return CommandResult{}, ErrKey
	}
	if old, ok := j.records[key]; ok {
		if old.Action != action {
			return CommandResult{}, ErrConflict
		}
		if old.Status == 0 {
			return old, ErrUnknown
		}
		old.Body = append(json.RawMessage(nil), old.Body...)
		return old, nil
	}
	next := make(map[string]CommandResult, len(j.records)+1)
	for k, r := range j.records {
		if j.validKey(k) {
			next[k] = r
		}
	}
	if len(next) >= MaxCommands {
		return CommandResult{}, ErrCapacity
	}
	result := CommandResult{Key: key, Action: action, Started: j.now().Unix()}
	next[key] = result
	if err := j.save(next); err != nil {
		return result, err
	}
	status, body := run()
	if status < 200 || status > 599 || len(body) > 16*1024 || (len(body) > 0 && !json.Valid(body)) {
		return result, ErrUnknown
	}
	result.Status = status
	result.Body = append(json.RawMessage(nil), body...)
	// Do not mutate the map published by the intent save before persistence.
	completed := make(map[string]CommandResult, len(next))
	for k, r := range next {
		completed[k] = r
	}
	completed[key] = result
	if err := j.save(completed); err != nil {
		return result, err
	}
	return result, nil
}

func (j *Journal) Get(key string) (CommandResult, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.records[key]
	r.Body = append(json.RawMessage(nil), r.Body...)
	return r, ok
}
