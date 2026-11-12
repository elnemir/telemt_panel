package store

import (
	"errors"
	"maps"
	"slices"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
)

const MaxManagedAdmins = 100

var (
	ErrAdminExists   = errors.New("administrator already exists")
	ErrAdminLimit    = errors.New("administrator limit reached")
	ErrAdminNotFound = errors.New("administrator not found")
)

func splitAdminSessions(sessions map[string]Session) (owner, managed map[string]Session) {
	owner = make(map[string]Session)
	managed = make(map[string]Session)
	for id, session := range sessions {
		if session.Username == "" {
			owner[id] = session
		} else {
			managed[id] = session
		}
	}
	return owner, managed
}

func (m *Memory) AdminPassword(username, owner, configuredHash string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if username == owner {
		if m.ownerPassword != "" {
			return m.ownerPassword
		}
		return configuredHash
	}
	return m.adminPasswords[username]
}

func (m *Memory) ListAdmins() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.adminPasswords))
}

func (m *Memory) CreateAdmin(username, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.adminPasswords[username]; exists {
		return ErrAdminExists
	}
	if len(m.adminPasswords) >= MaxManagedAdmins {
		return ErrAdminLimit
	}
	m.adminPasswords[username] = hash
	if err := m.writeStateLocked(); err != nil {
		if !atomicfile.Published(err) {
			delete(m.adminPasswords, username)
		}
		return err
	}
	return nil
}

func (m *Memory) ChangeAdminPassword(username, owner, hash string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if username == owner {
		previous := m.ownerPassword
		m.ownerPassword = hash
		return m.persistAdminChangeLocked("", "", func() { m.ownerPassword = previous })
	}
	if previous, ok := m.adminPasswords[username]; ok {
		m.adminPasswords[username] = hash
		return m.persistAdminChangeLocked(username, "", func() { m.adminPasswords[username] = previous })
	}
	return nil, ErrAdminNotFound
}

func (m *Memory) DeleteAdmin(username string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, ok := m.adminPasswords[username]
	if !ok {
		return nil, ErrAdminNotFound
	}
	delete(m.adminPasswords, username)
	return m.persistAdminChangeLocked(username, "", func() { m.adminPasswords[username] = previous })
}

func (m *Memory) DeleteAdminSessions(username, keep string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistAdminChangeLocked(username, keep, nil)
}

// Account changes and their session revocations share one disk publication.
func (m *Memory) persistAdminChangeLocked(username, keep string, rollback func()) ([]string, error) {
	removed := make(map[string]Session)
	for id, session := range m.sessions {
		if session.Username == username && id != keep {
			removed[id] = session
			delete(m.sessions, id)
		}
	}
	if len(removed) == 0 && rollback == nil {
		return nil, nil
	}
	err := m.writeStateLocked()
	if err != nil && !atomicfile.Published(err) {
		maps.Copy(m.sessions, removed)
		if rollback != nil {
			rollback()
		}
		return nil, err
	}
	return slices.Collect(maps.Keys(removed)), err
}
