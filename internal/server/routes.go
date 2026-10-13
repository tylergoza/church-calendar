package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Static assets & PWA plumbing
	mux.Handle("GET /static/", s.staticHandler())
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.asset("icons/icon.svg"), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /offline", s.handleOffline)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.DB.PingContext(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	// Public
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /setup", s.handleSetupForm)
	mux.HandleFunc("POST /setup", s.handleSetup)

	// Signed-in members.
	auth := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(h)) }

	// The calendar is public: no requireUser. Signed-in people get the same
	// pages with more detail (the session is loaded for these paths).
	mux.HandleFunc("GET /{$}", s.handleCalendar)
	mux.HandleFunc("GET /calendar", s.handleCalendar)
	mux.HandleFunc("GET /calendar/{id}", s.handleBookingShow)
	mux.HandleFunc("GET /calendar/by-source/{source}/{source_id}", s.handleBookingBySource)

	// Room requests: signed-in people, and visitors when public requests
	// are on (the handlers check). The status page needs the link's code.
	mux.HandleFunc("GET /request", s.handleRequestNew)
	mux.HandleFunc("POST /request", s.handleRequestCreate)
	mux.HandleFunc("GET /request/availability", s.handleRequestAvailability)
	mux.HandleFunc("GET /request/{id}", s.handleRequestStatus)
	mux.HandleFunc("POST /request/{id}/withdraw", s.handleRequestWithdraw)

	auth("GET /account", s.handleAccount)
	auth("POST /account/password", s.handleAccountPassword)
	auth("GET /rooms", s.handleRooms)
	auth("GET /rooms/{id}", s.handleRoomShow)

	// Schedulers
	sched := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireScheduler(h)) }
	sched("GET /rooms/new", s.handleRoomNew)
	sched("POST /rooms", s.handleRoomCreate)
	sched("GET /rooms/{id}/edit", s.handleRoomEdit)
	sched("POST /rooms/{id}", s.handleRoomUpdate)
	sched("GET /bookings/new", s.handleBookingNew)
	sched("POST /bookings", s.handleBookingCreate)
	sched("GET /bookings/{id}/edit", s.handleBookingEdit)
	sched("POST /bookings/{id}", s.handleBookingUpdate)
	sched("POST /bookings/{id}/delete", s.handleBookingDelete)
	sched("POST /bookings/{id}/end-series", s.handleBookingEndSeries)
	sched("GET /requests", s.handleRequests)
	sched("GET /requests/{id}", s.handleRequestShow)
	sched("POST /requests/{id}/approve", s.handleRequestApprove)
	sched("POST /requests/{id}/decline", s.handleRequestDecline)
	sched("GET /conflicts", s.handleConflicts)
	sched("POST /conflicts/ok", s.handleConflictOK)

	// Admins
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAdmin(h)) }
	admin("GET /admin/users", s.handleUsers)
	admin("GET /admin/users/new", s.handleUserNew)
	admin("POST /admin/users", s.handleUserCreate)
	admin("GET /admin/users/{id}/edit", s.handleUserEdit)
	admin("POST /admin/users/{id}", s.handleUserUpdate)
	admin("POST /admin/users/{id}/delete", s.handleUserDelete)
	admin("GET /admin/settings", s.handleSettings)
	admin("POST /admin/settings", s.handleSettingsUpdate)
	admin("GET /admin/backup", s.handleBackup)
	admin("GET /admin/connections", s.handleConnections)
	admin("POST /admin/connections/{id}", s.handleConnectionSave)
	admin("POST /admin/connections/{id}/test", s.handleConnectionTest)
	admin("POST /admin/connections/{id}/keep", s.handleConnectionKeep)
	admin("POST /admin/connections/{id}/clear", s.handleConnectionClear)
	admin("GET /admin/tokens", s.handleTokens)
	admin("POST /admin/tokens", s.handleTokenCreate)
	admin("POST /admin/tokens/{id}/private", s.handleTokenPrivate)
	admin("POST /admin/tokens/{id}/delete", s.handleTokenDelete)

	s.apiRoutes(mux)

	mux.HandleFunc("/", s.notFound)

	pages := s.loadSession(s.csrf(mux))
	return s.logRequests(s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isCookieFree(r.URL.Path):
			// The API and the feed ignore sessions and CSRF: no session is
			// looked up and no cookie is set.
			mux.ServeHTTP(w, r)
		default:
			if isPublicPath(r.URL.Path) {
				w.Header().Add("Vary", "Cookie") // signed-in people see more
			}
			pages.ServeHTTP(w, r)
		}
	})))
}

// isCookieFree paths ignore sessions and CSRF: the API and the feeds.
func isCookieFree(path string) bool {
	return path == "/calendar.ics" || strings.HasPrefix(path, "/api/")
}

// isPublicPath paths are open to visitors; signed-in people see more.
func isPublicPath(path string) bool {
	return path == "/" || path == "/calendar" || strings.HasPrefix(path, "/calendar/") ||
		path == "/request" || strings.HasPrefix(path, "/request/") || isCookieFree(path)
}

// Middleware -------------------------------------------------------------

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		defer func() {
			if err := recover(); err != nil {
				s.log.Error("panic", "path", r.URL.Path, "err", err)
				http.Error(rec, "internal error", http.StatusInternalServerError)
			}
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
					"dur", time.Since(start).Round(time.Millisecond), "ip", s.clientIP(r))
			}
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush and write deadlines.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", s.csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

const sessionCookie = "cal_session"

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			sess, err := s.store.GetSession(c.Value)
			var user *store.User
			if err == nil {
				user = &sess.User
			}
			if user != nil {
				r = withValue(r, ctxSession, sess)
				r = withValue(r, ctxUser, user)
			} else {
				http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrf implements the double-submit cookie pattern: every visitor gets a
// random token cookie and every POST must echo it in the _csrf field (or
// X-CSRF-Token header). Combined with SameSite=Lax cookies this blocks
// cross-site form posts.
const csrfCookie = "cal_csrf"

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) >= 32 {
			token = c.Value
		}
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			sent := r.Header.Get("X-CSRF-Token")
			if sent == "" {
				sent = r.PostFormValue("_csrf")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
				s.render(w, r, http.StatusForbidden, "error", map[string]any{
					"Title":   "Form expired",
					"Message": "Your form session expired. Please go back, refresh the page, and try again.",
				})
				return
			}
		}
		if token == "" {
			token = randomToken()
			http.SetCookie(w, &http.Cookie{
				Name: csrfCookie, Value: token, Path: "/", HttpOnly: true,
				SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r), MaxAge: 365 * 24 * 3600,
			})
		}
		next.ServeHTTP(w, withValue(r, ctxCSRF, token))
	})
}

func (s *Server) requireUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r) == nil {
			dest := r.URL.RequestURI()
			if r.Method != http.MethodGet {
				dest = "/"
			}
			http.Redirect(w, r, "/login?next="+urlEscape(dest), http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		next(w, r)
	})
}

func (s *Server) requireScheduler(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).CanSchedule() {
			s.render(w, r, http.StatusForbidden, "error", map[string]any{
				"Title": "Schedulers only", "Message": "You need to be a scheduler or administrator to do that.",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.Handler {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !currentUser(r).IsAdmin() {
			s.render(w, r, http.StatusForbidden, "error", map[string]any{
				"Title": "Admins only", "Message": "You need administrator access for that page.",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// baseURL is this app's address as the browser sees it, for feed links.
func (s *Server) baseURL(r *http.Request) string {
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host, _, _ = strings.Cut(h, ",")
			host = strings.TrimSpace(host)
		}
	}
	return scheme + "://" + host
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
