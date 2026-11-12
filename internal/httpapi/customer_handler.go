package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/amirotin/telemt_panel/internal/auth"
	"github.com/amirotin/telemt_panel/internal/customer"
	"github.com/amirotin/telemt_panel/internal/payments"
	"github.com/amirotin/telemt_panel/internal/platform"
)

const customerCookieName = "telemt_customer_session"
const customerOIDCCookieName = "telemt_customer_oidc"

type customerHandler func(http.ResponseWriter, *http.Request, platform.CustomerSession)

func (s *Server) registerCustomerRoutes(mux *http.ServeMux) {
	public := func(h http.HandlerFunc) http.Handler { return auth.CSRF(s.cfg)(h) }
	protect := func(h customerHandler) http.Handler {
		return public(func(w http.ResponseWriter, r *http.Request) {
			if !s.requireCustomerPortal(w) {
				return
			}
			session, ok := s.customerSession(w, r)
			if !ok {
				return
			}
			h(w, r, session)
		})
	}
	mux.HandleFunc("GET /api/customer/auth/methods", s.handleCustomerMethods)
	mux.Handle("POST /api/customer/auth/login", public(s.handleCustomerLogin))
	mux.Handle("POST /api/customer/auth/register", public(s.handleCustomerRegister))
	mux.Handle("POST /api/customer/auth/recover", public(s.handleCustomerRecover))
	mux.Handle("POST /api/customer/auth/email/complete", public(s.handleCustomerEmailComplete))
	mux.Handle("POST /api/customer/auth/telegram/start", public(s.handleCustomerTelegramStart))
	mux.HandleFunc("GET /api/customer/auth/telegram/callback", s.handleCustomerTelegramCallback)
	mux.Handle("POST /api/customer/auth/email/link", protect(s.handleCustomerEmailLink))
	mux.Handle("POST /api/customer/auth/logout", protect(s.handleCustomerLogout))
	mux.Handle("GET /api/customer/auth/me", protect(s.handleCustomerMe))
	mux.Handle("GET /api/customer/plans", protect(s.handleCustomerPlans))
	mux.Handle("GET /api/customer/payment-options", protect(s.handleCustomerPaymentOptions))
	mux.Handle("GET /api/customer/subscriptions", protect(s.handleCustomerSubscriptions))
	mux.Handle("GET /api/customer/subscriptions/{subscription_id}", protect(s.handleCustomerSubscription))
	mux.Handle("GET /api/customer/subscriptions/{subscription_id}/periods", protect(s.handleCustomerPeriods))
	mux.Handle("GET /api/customer/invoices", protect(s.handleCustomerInvoices))
	mux.Handle("POST /api/customer/invoices", protect(s.handleCustomerCreateInvoice))
	mux.Handle("GET /api/customer/invoices/{invoice_id}", protect(s.handleCustomerInvoice))
	mux.Handle("POST /api/customer/invoices/{invoice_id}/checkout", protect(s.handleCustomerCheckout))
}
func writeCustomerError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, platform.ErrCustomerAuth), errors.Is(e, customer.ErrTelegram):
		auth.WriteError(w, 401, "customer_auth_failed", "customer authentication failed")
	case errors.Is(e, customer.ErrInput), errors.Is(e, payments.ErrInvalid):
		auth.WriteError(w, 400, "customer_invalid", "invalid customer input")
	case errors.Is(e, customer.ErrLimited):
		w.Header().Set("Retry-After", "60")
		auth.WriteError(w, 429, "rate_limited", "too many authentication requests")
	case errors.Is(e, customer.ErrUnavailable):
		auth.WriteError(w, 503, "customer_method_unavailable", "customer login method is not configured")
	case errors.Is(e, customer.ErrDelivery):
		auth.WriteError(w, 503, "customer_email_delivery_failed", "verification message could not be delivered")
	case errors.Is(e, platform.ErrPaymentConfiguration):
		auth.WriteError(w, 503, "payment_configuration_unavailable", "payment configuration unavailable")
	default:
		writePlatformError(w, e)
	}
}
func (s *Server) requireCustomerPortal(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.customer == nil {
		writeCustomerError(w, customer.ErrUnavailable)
		return false
	}
	return true
}
func (s *Server) customerSession(w http.ResponseWriter, r *http.Request) (platform.CustomerSession, bool) {
	cookie, e := r.Cookie(customerCookieName)
	if e != nil || len(cookie.Value) != 43 {
		writeCustomerError(w, platform.ErrCustomerAuth)
		return platform.CustomerSession{}, false
	}
	session, e := s.customer.Store.ResolveCustomerSession(r.Context(), auth.HashToken(cookie.Value))
	if e != nil {
		writeCustomerError(w, e)
		return session, false
	}
	return session, true
}
func (s *Server) setCustomerCookie(w http.ResponseWriter, r *http.Request, name, token string, ttl int, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: s.cfg.BasePath + "/", HttpOnly: true, Secure: auth.RequestIsSecure(r, s.cfg.TrustedProxyPrefixes), SameSite: sameSite, MaxAge: ttl})
}
func (s *Server) customerLoginResult(w http.ResponseWriter, r *http.Request, token string, session platform.CustomerSession, e error) {
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	s.setCustomerCookie(w, r, customerCookieName, token, int(platform.CustomerSessionTTL), http.SameSiteStrictMode)
	writeJSON(w, 200, session.Customer)
}
func (s *Server) handleCustomerMethods(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	value := map[string]bool{"enabled": s.customer != nil, "email": false, "email_delivery": false, "telegram": false}
	if s.customer != nil {
		value["email"] = s.customer.Config.Email.Enabled
		value["email_delivery"] = s.customer.EmailAvailable()
		value["telegram"] = s.customer.TelegramAvailable()
	}
	writeJSON(w, 200, value)
}

type customerEmailInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleCustomerLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requireCustomerPortal(w) {
		return
	}
	var in customerEmailInput
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 2048, RejectUnknown: true}) != nil {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	token, session, e := s.customer.Login(r.Context(), auth.ClientIP(r, s.cfg.TrustedProxyPrefixes), in.Email, in.Password)
	s.customerLoginResult(w, r, token, session, e)
}
func (s *Server) handleCustomerRegister(w http.ResponseWriter, r *http.Request) {
	s.sendCustomerEmail(w, r, "register", "")
}
func (s *Server) handleCustomerRecover(w http.ResponseWriter, r *http.Request) {
	s.sendCustomerEmail(w, r, "reset", "")
}
func (s *Server) handleCustomerEmailLink(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	s.sendCustomerEmail(w, r, "link", session.IDHash)
}
func (s *Server) sendCustomerEmail(w http.ResponseWriter, r *http.Request, purpose, sessionHash string) {
	if !s.requireCustomerPortal(w) {
		return
	}
	var in customerEmailInput
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 2048, RejectUnknown: true}) != nil {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	if e := s.customer.SendEmailChallenge(r.Context(), auth.ClientIP(r, s.cfg.TrustedProxyPrefixes), purpose, in.Email, in.Password, sessionHash); e != nil {
		writeCustomerError(w, e)
		return
	}
	w.WriteHeader(202)
}
func (s *Server) handleCustomerEmailComplete(w http.ResponseWriter, r *http.Request) {
	if !s.requireCustomerPortal(w) {
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 2048, RejectUnknown: true}) != nil {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	token, session, e := s.customer.CompleteEmail(r.Context(), auth.ClientIP(r, s.cfg.TrustedProxyPrefixes), in.Token, in.Password)
	s.customerLoginResult(w, r, token, session, e)
}
func (s *Server) handleCustomerMe(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	writeJSON(w, 200, session.Customer)
}
func (s *Server) handleCustomerLogout(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	if e := s.customer.Store.RevokeCustomerSession(r.Context(), session.IDHash); e != nil {
		writeCustomerError(w, e)
		return
	}
	s.setCustomerCookie(w, r, customerCookieName, "", -1, http.SameSiteStrictMode)
	w.WriteHeader(204)
}
func (s *Server) handleCustomerTelegramStart(w http.ResponseWriter, r *http.Request) {
	if !s.requireCustomerPortal(w) {
		return
	}
	var in struct {
		Link bool `json:"link"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	hash := ""
	if in.Link {
		session, ok := s.customerSession(w, r)
		if !ok {
			return
		}
		hash = session.IDHash
	}
	start, e := s.customer.StartTelegram(r.Context(), auth.ClientIP(r, s.cfg.TrustedProxyPrefixes), hash)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	s.setCustomerCookie(w, r, customerOIDCCookieName, start.BrowserToken, 600, http.SameSiteLaxMode)
	writeJSON(w, 200, map[string]string{"url": start.URL})
}
func (s *Server) handleCustomerTelegramCallback(w http.ResponseWriter, r *http.Request) {
	if !s.requireCustomerPortal(w) {
		return
	}
	s.setCustomerCookie(w, r, customerOIDCCookieName, "", -1, http.SameSiteLaxMode)
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil || len(q) > 8 {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	for _, v := range q {
		if len(v) != 1 {
			writeCustomerError(w, customer.ErrInput)
			return
		}
	}
	browser, e := r.Cookie(customerOIDCCookieName)
	if e != nil || q.Get("error") != "" {
		http.Redirect(w, r, s.customer.PortalURL()+"#auth_error=telegram", http.StatusSeeOther)
		return
	}
	token, _, e := s.customer.CompleteTelegram(r.Context(), auth.ClientIP(r, s.cfg.TrustedProxyPrefixes), q.Get("state"), browser.Value, q.Get("code"))
	if e != nil {
		http.Redirect(w, r, s.customer.PortalURL()+"#auth_error=telegram", http.StatusSeeOther)
		return
	}
	s.setCustomerCookie(w, r, customerCookieName, token, int(platform.CustomerSessionTTL), http.SameSiteStrictMode)
	http.Redirect(w, r, s.customer.PortalURL(), http.StatusSeeOther)
}
func (s *Server) handleCustomerPlans(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	list, e := s.customer.Store.ListPlans(r.Context(), "", true)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleCustomerPaymentOptions(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	accounts, e := s.customer.Store.ListPaymentAccounts(r.Context())
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	out := []map[string]string{}
	for _, a := range accounts {
		if a.Enabled && !a.Test {
			out = append(out, map[string]string{"id": a.ID, "name": a.Name, "provider": a.Provider, "currency": "RUB"})
		}
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleCustomerSubscriptions(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	offset, ok := billingOffset(w, r)
	if !ok {
		return
	}
	list, e := s.customer.Store.CustomerSubscriptions(r.Context(), session.Customer.ID, offset)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleCustomerSubscription(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	sub, e := s.customer.Store.CustomerSubscription(r.Context(), session.Customer.ID, r.PathValue("subscription_id"))
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	current, e := s.customer.Store.CurrentPaidPeriod(r.Context(), session.Customer.ID, sub.ID)
	if e != nil && !errors.Is(e, platform.ErrNotFound) {
		writeCustomerError(w, e)
		return
	}
	var period *platform.PaidPeriod
	if e == nil {
		period = &current
	}
	end, e := s.customer.Store.CustomerPaidThrough(r.Context(), session.Customer.ID, sub.ID)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	access, e := s.customer.Store.CustomerServiceAccess(r.Context(), session.Customer.ID, sub.ID)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"subscription": sub, "current_period": period, "paid_through": end, "server_time": time.Now().Unix(), "access_status": access.Status, "usage_available": access.Usage != nil && access.Usage.LastReportedAt != nil, "access": access})
}
func (s *Server) handleCustomerPeriods(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	offset, ok := billingOffset(w, r)
	if !ok {
		return
	}
	id := r.PathValue("subscription_id")
	if _, e := s.customer.Store.CustomerSubscription(r.Context(), session.Customer.ID, id); e != nil {
		writeCustomerError(w, e)
		return
	}
	list, e := s.customer.Store.CustomerPeriods(r.Context(), session.Customer.ID, id, offset)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleCustomerInvoices(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	offset, ok := billingOffset(w, r)
	if !ok {
		return
	}
	list, e := s.customer.Store.ListCustomerInvoices(r.Context(), session.Customer.ID, offset)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, list)
}
func (s *Server) handleCustomerInvoice(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	invoice, e := s.customer.Store.CustomerInvoice(r.Context(), session.Customer.ID, r.PathValue("invoice_id"))
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, invoice)
}
func (s *Server) handleCustomerCreateInvoice(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	var in platform.InvoiceInput
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 2048, RejectUnknown: true}) != nil {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	account, e := s.customer.Store.GetPaymentAccount(r.Context(), in.AccountID)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	if account.Test {
		writeCustomerError(w, platform.ErrNotFound)
		return
	}
	invoice, e := s.customer.Store.CreateInvoice(r.Context(), session.Customer.ID, in)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 201, invoice)
}
func (s *Server) handleCustomerCheckout(w http.ResponseWriter, r *http.Request, session platform.CustomerSession) {
	var in struct {
		Method string `json:"method"`
	}
	if decodeJSONBody(w, r, &in, jsonBodyOptions{MaxBytes: 1024, RejectUnknown: true}) != nil {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	if in.Method != "AC" && in.Method != "PC" {
		writeCustomerError(w, customer.ErrInput)
		return
	}
	// Return destination is configuration-owned. Never accept browser return URLs.
	returnURL := strings.TrimRight(s.customer.Config.PublicURL, "/") + "/portal"
	form, e := s.customer.Store.InvoiceCheckout(r.Context(), session.Customer.ID, r.PathValue("invoice_id"), in.Method, returnURL)
	if e != nil {
		writeCustomerError(w, e)
		return
	}
	writeJSON(w, 200, form)
}
