//go:build !lite

package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const customerTestHash = "$2a$10$Ph6q3hjW8AtIjY.5GquOtOrqc7qWvB.h31kXJw0nDJAWSgXRPekmq"

func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func customerFixture(t *testing.T) (*Store, CustomerCredential, *int64) {
	t.Helper()
	s, _, _ := testStore(t)
	now := int64(2000000000)
	s.now = func() time.Time { return time.Unix(now, 0) }
	if e := s.PrepareEmailChallenge(context.Background(), "register", "Test@EXAMPLE.test", tokenHash("verify"), customerTestHash, ""); e != nil {
		t.Fatal(e)
	}
	if countTable(t, s, "customers") != 0 {
		t.Fatal("unverified customer created")
	}
	c, e := s.CompleteEmailChallenge(context.Background(), tokenHash("verify"), "")
	if e != nil {
		t.Fatal(e)
	}
	return s, c, &now
}
func TestEmailProofSingleUseConcurrencyAndSessionScope(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	if e := s.PrepareEmailChallenge(ctx, "register", "Case@Example.test", tokenHash("proof"), customerTestHash, ""); e != nil {
		t.Fatal(e)
	}
	var successes atomic.Int32
	var c CustomerCredential
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, e := s.CompleteEmailChallenge(ctx, tokenHash("proof"), "")
			if e == nil {
				successes.Add(1)
				mu.Lock()
				c = got
				mu.Unlock()
			} else if !errors.Is(e, ErrCustomerAuth) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 || countTable(t, s, "customers") != 1 || c.Customer.Email != "case@example.test" || !c.Customer.EmailVerified {
		t.Fatal(successes.Load(), c)
	}
	if e := s.PrepareEmailChallenge(ctx, "register", "CASE@EXAMPLE.TEST", tokenHash("another"), customerTestHash, ""); !errors.Is(e, ErrConflict) {
		t.Fatal("duplicate customer", e)
	}
	session, e := s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, tokenHash("session"))
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.ResolveCustomerSession(ctx, session.IDHash)
	if e != nil || got.Customer.ID != c.Customer.ID {
		t.Fatal(got, e)
	}
	if _, e = s.ResolveCustomerSession(ctx, tokenHash("admin-session")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal(e)
	}
	if e = s.RevokeCustomerSession(ctx, session.IDHash); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ResolveCustomerSession(ctx, session.IDHash); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("revoked session usable", e)
	}
}
func TestResetInvalidatesSessionsAndInFlightPasswordSnapshot(t *testing.T) {
	s, c, now := customerFixture(t)
	ctx := context.Background()
	if _, e := s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, tokenHash("old-session")); e != nil {
		t.Fatal(e)
	}
	if e := s.PrepareEmailChallenge(ctx, "reset", c.Customer.Email, tokenHash("reset"), "", ""); e != nil {
		t.Fatal(e)
	}
	updated, e := s.CompleteEmailChallenge(ctx, tokenHash("reset"), customerTestHash)
	if e != nil || updated.AuthRevision != c.AuthRevision+1 {
		t.Fatal(updated, e)
	}
	if _, e = s.ResolveCustomerSession(ctx, tokenHash("old-session")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal(e)
	}
	if _, e = s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, tokenHash("late-password-login")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("pre-reset password verification bypassed reset", e)
	}
	if _, e = s.CreateCustomerSession(ctx, c.Customer.ID, updated.AuthRevision, tokenHash("new-session")); e != nil {
		t.Fatal(e)
	}
	*now += CustomerSessionTTL
	if _, e = s.ResolveCustomerSession(ctx, tokenHash("new-session")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("expired session", e)
	}
}
func TestChallengesExpiryReplacementAndDisabledCustomers(t *testing.T) {
	s, c, now := customerFixture(t)
	ctx := context.Background()
	if e := s.PrepareEmailChallenge(ctx, "reset", c.Customer.Email, tokenHash("old"), "", ""); e != nil {
		t.Fatal(e)
	}
	if e := s.PrepareEmailChallenge(ctx, "reset", c.Customer.Email, tokenHash("new"), "", ""); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CompleteEmailChallenge(ctx, tokenHash("old"), customerTestHash); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("replaced token", e)
	}
	*now += 900
	if _, e := s.CompleteEmailChallenge(ctx, tokenHash("new"), customerTestHash); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("expired token", e)
	}
	if e := s.PrepareEmailChallenge(ctx, "reset", c.Customer.Email, tokenHash("disabled"), "", ""); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec("UPDATE customers SET enabled=0 WHERE id=?", c.Customer.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CompleteEmailChallenge(ctx, tokenHash("disabled"), customerTestHash); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("reset enabled disabled customer", e)
	}
}
func TestTelegramLinkRequiresFreshProofAndDoesNotMergeAccounts(t *testing.T) {
	s, c, now := customerFixture(t)
	ctx := context.Background()
	hash := tokenHash("fresh-session")
	if _, e := s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, hash); e != nil {
		t.Fatal(e)
	}
	other, e := s.TelegramCustomer(ctx, "222")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.LinkCustomerTelegram(ctx, hash, "222"); !errors.Is(e, ErrConflict) {
		t.Fatal("merged two customers", e)
	}
	*now += CustomerFreshAuth + 1
	if _, e = s.LinkCustomerTelegram(ctx, hash, "111"); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("stale link", e)
	}
	if _, e = s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, tokenHash("fresh-again")); e != nil {
		t.Fatal(e)
	}
	linked, e := s.LinkCustomerTelegram(ctx, tokenHash("fresh-again"), "111")
	if e != nil || linked.Customer.ID != c.Customer.ID || !linked.Customer.TelegramLinked {
		t.Fatal(linked, e)
	}
	same, e := s.TelegramCustomer(ctx, "111")
	if e != nil || same.Customer.ID != c.Customer.ID {
		t.Fatal("link created customer", same, e)
	}
	if _, e = s.ResolveCustomerSession(ctx, hash); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("old auth revision", e)
	}
	if _, e = s.CreateCustomerSession(ctx, other.Customer.ID, other.AuthRevision, tokenHash("tg-session")); e != nil {
		t.Fatal(e)
	}
	if e = s.PrepareEmailChallenge(ctx, "link", "other@example.test", tokenHash("email-link"), customerTestHash, tokenHash("tg-session")); e != nil {
		t.Fatal(e)
	}
	linkedEmail, e := s.CompleteEmailChallenge(ctx, tokenHash("email-link"), "")
	if e != nil || linkedEmail.Customer.ID != other.Customer.ID || !linkedEmail.Customer.EmailVerified || !linkedEmail.Customer.TelegramLinked {
		t.Fatal(linkedEmail, e)
	}
}
func TestOIDCStateBindingOneShotEncryptedVerifierAndLogoutFence(t *testing.T) {
	s, c, now := customerFixture(t)
	ctx := context.Background()
	session := tokenHash("session")
	if _, e := s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, session); e != nil {
		t.Fatal(e)
	}
	verifier := strings.Repeat("a", 43)
	if e := s.PutCustomerOIDCState(ctx, tokenHash("state"), tokenHash("browser"), verifier, tokenHash("nonce"), session); e != nil {
		t.Fatal(e)
	}
	var raw []byte
	if e := s.db.QueryRow("SELECT verifier_cipher FROM customer_oidc_states").Scan(&raw); e != nil || strings.Contains(string(raw), verifier) {
		t.Fatal("verifier stored plaintext", e)
	}
	if _, e := s.TakeCustomerOIDCState(ctx, tokenHash("state"), tokenHash("attacker")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal(e)
	}
	state, e := s.TakeCustomerOIDCState(ctx, tokenHash("state"), tokenHash("browser"))
	if e != nil || state.Verifier != verifier || state.NonceHash != tokenHash("nonce") || state.SessionHash != session {
		t.Fatal(state, e)
	}
	if _, e = s.TakeCustomerOIDCState(ctx, tokenHash("state"), tokenHash("browser")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("replay", e)
	}
	if e = s.PutCustomerOIDCState(ctx, tokenHash("logout-state"), tokenHash("browser"), verifier, tokenHash("nonce"), session); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeCustomerSession(ctx, session); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TakeCustomerOIDCState(ctx, tokenHash("logout-state"), tokenHash("browser")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("logged-out link", e)
	}
	if e = s.PutCustomerOIDCState(ctx, tokenHash("login-state"), tokenHash("browser"), verifier, tokenHash("nonce"), ""); e != nil {
		t.Fatal(e)
	}
	*now += 600
	if _, e = s.TakeCustomerOIDCState(ctx, tokenHash("login-state"), tokenHash("browser")); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("expired state", e)
	}
}
func TestCustomerSessionCapAndEmailNormalization(t *testing.T) {
	s, c, _ := customerFixture(t)
	ctx := context.Background()
	for i := range 12 {
		if _, e := s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, tokenHash(fmt.Sprint(i))); e != nil {
			t.Fatal(e)
		}
	}
	if countTable(t, s, "customer_sessions") != 10 {
		t.Fatal("unbounded sessions")
	}
	for _, email := range []string{"Name <test@example.test>", "теst@example.test", "test@example.test\r\nBCC: other@example.test", "\"quoted\"@example.test", "bad"} {
		if _, e := NormalizeCustomerEmail(email); e == nil {
			t.Fatal(email)
		}
	}
}

func TestCustomerAuthenticationStateSurvivesRestart(t *testing.T) {
	s, path, key := testStore(t)
	ctx := context.Background()
	if e := s.PrepareEmailChallenge(ctx, "register", "restart@example.test", tokenHash("proof-restart"), customerTestHash, ""); e != nil {
		t.Fatal(e)
	}
	verifier := strings.Repeat("b", 43)
	if e := s.PutCustomerOIDCState(ctx, tokenHash("state-restart"), tokenHash("browser-restart"), verifier, tokenHash("nonce-restart"), ""); e != nil {
		t.Fatal(e)
	}
	c, e := s.TelegramCustomer(ctx, "777")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CreateCustomerSession(ctx, c.Customer.ID, c.AuthRevision, tokenHash("session-restart")); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path, key)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if session, e := reopened.ResolveCustomerSession(ctx, tokenHash("session-restart")); e != nil || session.Customer.ID != c.Customer.ID {
		t.Fatal(session, e)
	}
	if state, e := reopened.TakeCustomerOIDCState(ctx, tokenHash("state-restart"), tokenHash("browser-restart")); e != nil || state.Verifier != verifier {
		t.Fatal(state, e)
	}
	verified, e := reopened.CompleteEmailChallenge(ctx, tokenHash("proof-restart"), "")
	if e != nil || verified.Customer.Email != "restart@example.test" {
		t.Fatal(verified, e)
	}
	if _, e = reopened.CompleteEmailChallenge(ctx, tokenHash("proof-restart"), ""); !errors.Is(e, ErrCustomerAuth) {
		t.Fatal("replayed persisted proof", e)
	}
}
