package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"unicode/utf8"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/store"
)

var adminUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type adminAccountInfo struct {
	Username string `json:"username"`
	Owner    bool   `json:"owner"`
	Current  bool   `json:"current"`
}

type adminAccountsResponse struct {
	Accounts []adminAccountInfo `json:"accounts"`
	Writable bool               `json:"writable"`
}

type adminChangeRequest struct {
	Username        string `json:"username,omitempty"`
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password,omitempty"`
}

func (s *Server) adminPassword(username string) string {
	return s.st.AdminPassword(username, s.cfg.Auth.Username, s.cfg.Auth.PasswordHash)
}

func (s *Server) sessionUsername(r *http.Request) string {
	username, _ := auth.UsernameFromContext(r.Context())
	if username == s.cfg.Auth.Username {
		return ""
	}
	return username
}

func (s *Server) ownerPasskeys(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.sessionUsername(r) != "" {
			auth.WriteError(w, http.StatusForbidden, "forbidden", "passkeys belong to the configured administrator")
			return
		}
		s.adminMu.Lock()
		defer s.adminMu.Unlock()
		id, _ := auth.SessionIDHashFromContext(r.Context())
		if err := s.sessions.Check(id); err != nil {
			auth.WriteError(w, http.StatusUnauthorized, "session_expired", "no valid session")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleListAdmins(w http.ResponseWriter, r *http.Request) {
	username, _ := auth.UsernameFromContext(r.Context())
	owner := s.cfg.Auth.Username
	accounts := []adminAccountInfo{{Username: owner, Owner: true, Current: username == owner}}
	for _, name := range s.st.ListAdmins() {
		accounts = append(accounts, adminAccountInfo{Username: name, Current: username == name})
	}
	writeJSON(w, http.StatusOK, adminAccountsResponse{Accounts: accounts, Writable: s.st.StateDurable()})
}

func (s *Server) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	s.changeAdminAccount(w, r, "admin.create", func(actor string, req adminChangeRequest) (string, error) {
		if !adminUsernamePattern.MatchString(req.Username) {
			return "", errAdminUsername
		}
		if req.Username == s.cfg.Auth.Username {
			return "", store.ErrAdminExists
		}
		return req.Username, nil
	}, func(username, hash string) ([]string, error) {
		return nil, s.st.CreateAdmin(username, hash)
	})
}

func (s *Server) handleChangeAdminPassword(w http.ResponseWriter, r *http.Request) {
	s.changeAdminAccount(w, r, "admin.password.change", func(actor string, _ adminChangeRequest) (string, error) {
		return actor, nil
	}, func(username, hash string) ([]string, error) {
		return s.st.ChangeAdminPassword(username, s.cfg.Auth.Username, hash)
	})
}

func (s *Server) handleDeleteAdmin(w http.ResponseWriter, r *http.Request) {
	s.changeAdminAccount(w, r, "admin.delete", func(actor string, _ adminChangeRequest) (string, error) {
		target := r.PathValue("username")
		if target == actor || target == s.cfg.Auth.Username {
			return "", errAdminProtected
		}
		return target, nil
	}, func(username, _ string) ([]string, error) {
		return s.st.DeleteAdmin(username)
	})
}

var (
	errAdminUsername  = errors.New("invalid administrator username")
	errAdminProtected = errors.New("the configured or current administrator cannot be deleted")
	errAdminReauth    = errors.New("current password is incorrect")
)

func (s *Server) changeAdminAccount(w http.ResponseWriter, r *http.Request, action string, target func(string, adminChangeRequest) (string, error), change func(string, string) ([]string, error)) {
	if !s.st.StateDurable() {
		auth.WriteError(w, http.StatusForbidden, "admin_storage_required", "persistent panel state is required")
		return
	}
	var req adminChangeRequest
	if err := decodeJSONBody(w, r, &req, jsonBodyOptions{MaxBytes: loginRequestBodyLimit}); err != nil {
		auth.WriteError(w, http.StatusBadRequest, "bad_request", "invalid request body")
		return
	}

	actor, _ := auth.UsernameFromContext(r.Context())
	id, _ := auth.SessionIDHashFromContext(r.Context())
	username, err := target(actor, req)
	if err != nil {
		writeAdminChangeError(w, err)
		return
	}
	setPassword := action != "admin.delete"
	if setPassword && (utf8.RuneCountInString(req.NewPassword) < 12 || len(req.NewPassword) > 72) {
		auth.WriteError(w, http.StatusBadRequest, "admin_password_policy", "new password must contain at least 12 characters and at most 72 bytes")
		return
	}

	finish, admitted := s.limiter.Acquire(auth.ClientIP(r, s.cfg.TrustedProxyPrefixes))
	if !admitted {
		auth.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many failed password attempts")
		return
	}
	defer finish(false)

	before := s.adminPassword(actor)
	if !auth.VerifyAccountPassword(before, req.CurrentPassword) {
		finish(true)
		writeAdminChangeError(w, errAdminReauth)
		return
	}
	var nextHash string
	if setPassword {
		nextHash, err = auth.HashPassword(req.NewPassword)
		if err != nil {
			writeAdminChangeError(w, err)
			return
		}
	}

	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	err = s.sessions.ApplyAdminChange(id, func(store.Session) ([]string, error) {
		if before != s.adminPassword(actor) {
			return nil, errAdminReauth
		}
		return change(username, nextHash)
	})
	if err != nil {
		writeAdminChangeError(w, err)
		return
	}
	s.appendAudit(r, action, username, "")
	if action == "admin.password.change" {
		auth.ClearSessionCookie(w, r, s.cfg)
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeAdminChangeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrSessionExpired):
		auth.WriteError(w, http.StatusUnauthorized, "session_expired", "no valid session")
	case errors.Is(err, errAdminReauth):
		auth.WriteError(w, http.StatusForbidden, "admin_password_incorrect", err.Error())
	case errors.Is(err, errAdminProtected):
		auth.WriteError(w, http.StatusForbidden, "admin_protected", err.Error())
	case errors.Is(err, errAdminUsername):
		auth.WriteError(w, http.StatusBadRequest, "admin_username_invalid", err.Error())
	case errors.Is(err, store.ErrAdminExists):
		auth.WriteError(w, http.StatusConflict, "admin_exists", err.Error())
	case errors.Is(err, store.ErrAdminLimit):
		auth.WriteError(w, http.StatusConflict, "admin_limit", err.Error())
	case errors.Is(err, store.ErrAdminNotFound):
		auth.WriteError(w, http.StatusNotFound, "admin_not_found", err.Error())
	default:
		slog.Error("persist administrator change", "err", err)
		auth.WriteError(w, http.StatusInternalServerError, "internal_error", "could not save administrator change")
	}
}
