package nodeagent

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testState struct {
	raw    string
	writes int
	fail   int
}

func (s *testState) StateDurable() bool                      { return true }
func (s *testState) GetSetting(string) (string, bool, error) { return s.raw, s.raw != "", nil }
func (s *testState) SetSetting(_ string, v string) error {
	s.writes++
	if s.writes == s.fail {
		return errors.New("disk unavailable")
	}
	s.raw = v
	return nil
}
func TestJournalConcurrentReplayAndRestart(t *testing.T) {
	state := &testState{}
	j, err := OpenJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	key := NewCommandKey()
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			r, e := j.Execute(key, "service/restart", func() (int, []byte) { calls.Add(1); return 202, nil })
			if e != nil || r.Status != 202 {
				t.Errorf("%+v %v", r, e)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	j, err = OpenJournal(state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.Execute(key, "service/restart", func() (int, []byte) { t.Fatal("replayed after restart"); return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	_, err = j.Execute(key, "service/stop", nil)
	if !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	j.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	_, err = j.Execute(key, "service/restart", nil)
	if !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
}
func TestJournalPersistenceFailures(t *testing.T) {
	for _, fail := range []int{1, 2} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			state := &testState{fail: fail}
			j, _ := OpenJournal(state)
			key := NewCommandKey()
			calls := 0
			_, err := j.Execute(key, "service/restart", func() (int, []byte) { calls++; return 202, nil })
			if !errors.Is(err, ErrJournal) || calls != fail-1 {
				t.Fatalf("calls %d err %v", calls, err)
			}
			if fail == 2 {
				j, err = OpenJournal(state)
				if err != nil {
					t.Fatal(err)
				}
				_, err = j.Execute(key, "service/restart", nil)
				if !errors.Is(err, ErrUnknown) {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestJournalInvalidResultStaysUnknown(t *testing.T) {
	j, _ := OpenJournal(&testState{})
	key := NewCommandKey()
	_, err := j.Execute(key, "restart", func() (int, []byte) { return 200, []byte("not JSON") })
	if !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	_, err = j.Execute(key, "restart", nil)
	if !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
}
