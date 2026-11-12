package httpapi

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/payments"
	"github.com/amirotin/telemt_panel/internal/platform"
)

func (s *Server) registerBillingRoutes(mux *http.ServeMux, protect func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/billing/invoices", protect(s.handleBillingInvoices))
	mux.Handle("GET /api/billing/payments", protect(s.handleBillingPayments))
	mux.Handle("GET /api/billing/plans", protect(s.handleListPlans))
	mux.Handle("POST /api/billing/plans", protect(s.handleCreatePlan))
	mux.Handle("GET /api/billing/plans/{plan_id}", protect(s.handleGetPlan))
	mux.Handle("PUT /api/billing/plans/{plan_id}", protect(s.handleUpdatePlan))
	mux.Handle("GET /api/billing/payment-accounts", protect(s.handleListPaymentAccounts))
	mux.Handle("POST /api/billing/payment-accounts", protect(s.handleCreatePaymentAccount))
	mux.Handle("GET /api/billing/payment-accounts/{account_id}", protect(s.handleGetPaymentAccount))
	mux.Handle("PUT /api/billing/payment-accounts/{account_id}", protect(s.handleUpdatePaymentAccount))
	// Provider authentication is the signature, not an administrator cookie.
	mux.HandleFunc("POST /api/payments/{account_id}/notify", s.handlePaymentNotification)
}
func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	plans, e := s.platform.ListPlans(r.Context(), r.URL.Query().Get("cluster_id"), false)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	writeJSON(w, 200, plans)
}
func (s *Server) handleGetPlan(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	p, e := s.platform.GetPlan(r.Context(), r.PathValue("plan_id"))
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, p.Revision)
	writeJSON(w, 200, p)
}
func (s *Server) handleCreatePlan(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	var in struct {
		ClusterID string              `json:"cluster_id"`
		Name      string              `json:"name"`
		Enabled   *bool               `json:"enabled"`
		Terms     *platform.PlanTerms `json:"terms"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 8192, RejectUnknown: true}) != nil || in.Enabled == nil || in.Terms == nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	p, e := s.platform.CreatePlan(r.Context(), in.ClusterID, in.Name, *in.Enabled, *in.Terms)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, p.Revision)
	writeJSON(w, 201, p)
}
func (s *Server) handleUpdatePlan(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    string              `json:"name"`
		Enabled *bool               `json:"enabled"`
		Terms   *platform.PlanTerms `json:"terms"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 8192, RejectUnknown: true}) != nil || in.Enabled == nil || in.Terms == nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	if e := s.platform.UpdatePlan(r.Context(), r.PathValue("plan_id"), rev, in.Name, *in.Enabled, *in.Terms); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handleListPaymentAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	list, e := s.platform.ListPaymentAccounts(r.Context())
	if e != nil {
		writePlatformError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleGetPaymentAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	a, e := s.platform.GetPaymentAccount(r.Context(), r.PathValue("account_id"))
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, a.Revision)
	writeJSON(w, 200, a)
}
func (s *Server) handleCreatePaymentAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	var in struct {
		Name     string                         `json:"name"`
		Provider string                         `json:"provider"`
		Test     *bool                          `json:"test"`
		Enabled  *bool                          `json:"enabled"`
		Config   *platform.PaymentAccountConfig `json:"config"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 8192, RejectUnknown: true}) != nil || in.Test == nil || in.Enabled == nil || in.Config == nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	a, e := s.platform.CreatePaymentAccount(r.Context(), in.Name, in.Provider, *in.Test, *in.Enabled, *in.Config)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	platformETag(w, a.Revision)
	writeJSON(w, 201, a)
}
func (s *Server) handleUpdatePaymentAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	rev, ok := platformRevision(w, r)
	if !ok {
		return
	}
	var in struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil || in.Enabled == nil {
		writePlatformError(w, platform.ErrInvalid)
		return
	}
	if e := s.platform.UpdatePaymentAccount(r.Context(), r.PathValue("account_id"), rev, in.Name, *in.Enabled); e != nil {
		writePlatformError(w, e)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) handlePaymentNotification(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	kind, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || kind != "application/x-www-form-urlencoded" {
		auth.WriteError(w, 415, "payment_invalid", "form content type required")
		return
	}
	// Do not merge URL query values into the signed body. Bound body and parsing
	// work before signature verification; no callback body or secret is logged.
	if r.URL.RawQuery != "" {
		auth.WriteError(w, 400, "payment_invalid", "query parameters are not accepted")
		return
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if e != nil {
		auth.WriteError(w, 400, "payment_invalid", "invalid notification")
		return
	}
	ack, e := s.platform.ReceivePayment(r.Context(), r.PathValue("account_id"), body)
	if e != nil {
		switch {
		case errors.Is(e, payments.ErrSignature):
			auth.WriteError(w, 403, "payment_signature_invalid", "invalid notification signature")
		case errors.Is(e, payments.ErrInvalid), errors.Is(e, payments.ErrUnsettled):
			auth.WriteError(w, 400, "payment_invalid", "invalid notification")
		case errors.Is(e, platform.ErrPaymentConfiguration):
			auth.WriteError(w, 503, "payment_configuration_unavailable", "payment configuration unavailable")
		default:
			writePlatformError(w, e)
		}
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, ack)
}

func billingOffset(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("offset")
	if raw == "" {
		return 0, true
	}
	offset, e := strconv.Atoi(raw)
	if e != nil || offset < 0 {
		writePlatformError(w, platform.ErrInvalid)
		return 0, false
	}
	return offset, true
}
func (s *Server) handleBillingInvoices(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	offset, ok := billingOffset(w, r)
	if !ok {
		return
	}
	list, e := s.platform.ListBillingInvoices(r.Context(), offset)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleBillingPayments(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w) {
		return
	}
	offset, ok := billingOffset(w, r)
	if !ok {
		return
	}
	list, e := s.platform.ListBillingPayments(r.Context(), offset)
	if e != nil {
		writePlatformError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
