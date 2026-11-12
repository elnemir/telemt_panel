package httpapi

import (
	"net/http"
	"strings"
)

// This allowlist deliberately excludes local host operations and global panel
// configuration. A new root route never becomes node-enabled automatically.
func (s *Server) nodeManagementHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/telemt/config/files", s.handleNodeConfigFile)
	mux.HandleFunc("POST /api/telemt/config/files/render", s.handleNodeConfigFile)
	mux.HandleFunc("POST /api/telemt/config/files/validate", s.handleNodeConfigFile)
	mux.HandleFunc("POST /api/telemt/config/files/apply", s.handleNodeConfigFile)
	mux.HandleFunc("GET /api/telemt/config/files/backups", s.handleNodeConfigFile)
	mux.HandleFunc("GET /api/telemt/config/files/backups/{key}", s.handleNodeConfigFile)
	mux.HandleFunc("DELETE /api/telemt/config/files/backups/{key}", s.handleNodeConfigFile)

	mux.HandleFunc("GET /api/agent/commands/{key}", s.handleNodeAgentCommand)
	mux.HandleFunc("GET /api/telemt/info", s.handleTelemtInfo)
	mux.HandleFunc("POST /api/users/operations/quota-reset/prepare", s.handlePrepareQuotaReset)
	mux.HandleFunc("POST /api/users/operations/quota-reset", s.handleStartQuotaReset)
	mux.HandleFunc("GET /api/users/operations/quota-reset", s.handleQuotaResetStatus)
	mux.HandleFunc("GET /api/settings/quota-schedule", s.handleQuotaSchedule)
	mux.HandleFunc("PUT /api/settings/quota-schedule", s.handleSaveQuotaSchedule)
	mux.HandleFunc("POST /api/settings/quota-schedule/preview", s.handlePreviewQuotaSchedule)
	mux.HandleFunc("GET /api/users/{username}/quota-schedule", s.handleQuotaSchedule)
	mux.HandleFunc("PUT /api/users/{username}/quota-schedule", s.handleSaveUserQuotaSchedule)
	mux.HandleFunc("GET /api/telemt/config", s.handleGetTelemtConfig)
	mux.HandleFunc("GET /api/telemt/web-access", s.handleGetTelemtWebAccess)
	mux.HandleFunc("PUT /api/telemt/web-access/users/{username}", s.handlePutTelemtUserWebAccess)
	mux.HandleFunc("GET /api/telemt/config/catalog", s.handleGetTelemtConfigCatalog)
	mux.HandleFunc("PATCH /api/telemt/config", s.handlePatchTelemtConfig)
	mux.HandleFunc("GET /api/telemt/config/toml", s.handleGetTelemtConfigTOML)
	mux.HandleFunc("POST /api/telemt/config/toml/preview", s.handlePreviewTelemtConfigTOML)
	mux.HandleFunc("PATCH /api/telemt/config/toml", s.handlePatchTelemtConfigTOML)
	mux.HandleFunc("POST /api/telemt/reload", s.handleTelemtReload)
	mux.HandleFunc("GET /api/telemt/reload/{id}", s.handleTelemtReloadStatus)
	mux.HandleFunc("POST /api/telemt/restart", s.handleNodeServiceAction)
	mux.HandleFunc("GET /api/telemt/service", s.handleNodeService)
	mux.HandleFunc("POST /api/telemt/start", s.handleNodeServiceAction)
	mux.HandleFunc("POST /api/telemt/stop", s.handleNodeServiceAction)
	mux.HandleFunc("GET /api/telemt/zero", s.handleGetTelemtZero)
	mux.HandleFunc("GET /api/telemt/tls-fingerprints", s.handleGetTelemtTLSFingerprints)
	mux.HandleFunc("GET /api/telemt/web/sessions", s.handleGetTelemtWebSessions)
	mux.HandleFunc("GET /api/telemt/web/sessions/{ref}", s.handleGetTelemtWebSession)
	mux.HandleFunc("POST /api/telemt/web/sessions/close", s.handlePostTelemtWebSessionsClose)
	mux.HandleFunc("GET /api/telemt/web/operations/{id}", s.handleGetTelemtWebOperation)
	mux.HandleFunc("GET /api/audit", s.handleGetAudit)
	mux.HandleFunc("GET /api/history", s.handleGetHistory)
	mux.HandleFunc("GET /api/history/events", s.handleGetHistoryEvents)
	mux.HandleFunc("GET /api/users/{username}/traffic-history", s.handleGetUserTrafficHistory)
	mux.HandleFunc("GET /api/users/{username}/ip-history", s.handleGetUserIPHistory)
	mux.HandleFunc("POST /api/users/{username}/ip-history/reset", s.handleResetUserIPHistory)
	mux.HandleFunc("POST /api/users/{username}/traffic/reset", s.handleResetUserTraffic)
	mux.HandleFunc("GET /api/traffic/summary", s.handleGetTrafficSummary)
	mux.HandleFunc("GET /api/traffic/users", s.handleGetTrafficUsers)
	mux.HandleFunc("POST /api/traffic/reset", s.handleResetAllUserTraffic)
	mux.HandleFunc("GET /api/settings/links", s.handleGetLinkSettings)
	mux.HandleFunc("PUT /api/settings/links", s.handlePutLinkSettings)
	mux.HandleFunc("GET /api/settings/storage", s.handleGetStorageSettings)
	mux.HandleFunc("PUT /api/settings/storage", s.handlePutStorageSettings)
	mux.HandleFunc("POST /api/settings/storage/purge", s.handlePurgeStorageHistory)
	mux.HandleFunc("GET /api/geography", s.handleGetGeography)
	mux.HandleFunc("GET /api/geography/locations", s.handleGetGeographyLocations)
	mux.HandleFunc("GET /api/geography/users", s.handleGetGeographyUsers)
	mux.HandleFunc("GET /api/settings/geography", s.handleGetGeographySettings)
	mux.HandleFunc("PUT /api/settings/geography", s.handlePutGeographySettings)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /api/users", s.handleListUsers)
	mux.HandleFunc("POST /api/users", s.handleCreateUser)
	mux.HandleFunc("GET /api/users/{username}", s.handleGetUser)
	mux.HandleFunc("PATCH /api/users/{username}", s.handlePatchUser)
	mux.HandleFunc("DELETE /api/users/{username}", s.handleDeleteUser)
	mux.HandleFunc("POST /api/users/{username}/reset-quota", s.handleResetQuota)
	mux.HandleFunc("POST /api/users/{username}/rotate-secret", s.handleRotateSecret)
	mux.HandleFunc("PUT /api/users/{username}/enabled", s.handleSetEnabled)
	mux.HandleFunc("GET /api/users/{username}/sublink", nodeOperationUnavailable)
	mux.HandleFunc("POST /api/users/{username}/sublink", nodeOperationUnavailable)
	mux.HandleFunc("GET /api/host", s.handleNodeHost)
	mux.HandleFunc("/api/updates", nodeOperationUnavailable)
	mux.HandleFunc("/api/updates/{path...}", nodeOperationUnavailable)
	mux.HandleFunc("GET /api/logs/tail", s.handleNodeAgentLogs)
	mux.HandleFunc("HEAD /api/events/logs", s.handleNodeAgentLogs)
	mux.HandleFunc("GET /api/events/logs", s.handleNodeAgentLogs)
	return apiJSONFallback(mux)
}

// defaultNodeAPI identifies the legacy node-owned surface, not global panel
// auth/access/branding/registry/GeoIP assets. It never redirects to a node.
func defaultNodeAPI(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/api/"), "/")
	if !strings.HasPrefix(path, "/api/") || len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "users", "telemt", "history", "traffic", "geography", "audit", "events", "snapshot", "logs", "updates":
		return true
	case "settings":
		if len(parts) > 1 {
			switch parts[1] {
			case "storage", "links", "quota-schedule", "geography":
				return true
			}
		}
	}
	return false
}
