package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/amirotin/telemt_panel/internal/atomicfile"
)

const passwordIdentityKey = "auth.password_identity.v1"

// BindPasswordAuth binds persisted sessions to the configured admin credentials.
// It runs before serving requests. Legacy sessions have no binding and are
// revoked once. Changing the config restores the configured password and
// discards any override. The fingerprint cannot authenticate a user.
func (m *Memory) BindPasswordAuth(username, passwordHash string) error {
	raw, _ := json.Marshal([2]string{username, passwordHash})
	digest := sha256.Sum256(raw)
	identity := hex.EncodeToString(digest[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	previous, existed := m.settings[passwordIdentityKey]
	if previous == identity {
		return nil
	}
	sessions, challenges := m.sessions, m.webauthnChallenges
	ownerPassword, managedPassword := m.ownerPassword, m.adminPasswords[username]
	m.ownerPassword = ""
	delete(m.adminPasswords, username)
	m.sessions = make(map[string]Session)
	m.webauthnChallenges = make(map[string]WebAuthnChallenge)
	m.settings[passwordIdentityKey] = identity
	if err := m.writeStateLocked(); err != nil {
		if !atomicfile.Published(err) {
			m.sessions, m.webauthnChallenges = sessions, challenges
			m.ownerPassword = ownerPassword
			if managedPassword != "" {
				m.adminPasswords[username] = managedPassword
			}
			if existed {
				m.settings[passwordIdentityKey] = previous
			} else {
				delete(m.settings, passwordIdentityKey)
			}
		}
		return err
	}
	return nil
}
