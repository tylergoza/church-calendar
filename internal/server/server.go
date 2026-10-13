// Package server implements the HTTP interface: routing, sessions, HTML
// rendering and request handlers.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tylergoza/church-calendar/internal/connections"
	"github.com/tylergoza/church-calendar/internal/sso"
	"github.com/tylergoza/church-calendar/internal/store"
	"github.com/tylergoza/church-calendar/web"
)

type Config struct {
	// Dev serves templates and static files from ./web on disk and
	// re-parses templates on every request, so edits show up on refresh.
	Dev bool
	// TrustProxy uses X-Forwarded-For / X-Forwarded-Proto from a reverse
	// proxy (Caddy, nginx, a DO load balancer) for client IP and HTTPS.
	TrustProxy bool
	SessionTTL time.Duration
	// Env looks up environment variables that override the connections
	// saved on /admin/connections. os.Getenv when nil.
	Env func(string) string
}

type Server struct {
	cfg    Config
	store  *store.Store
	log    *slog.Logger
	webFS  fs.FS
	static fs.FS

	// Hash of all static assets, used for cache busting and the service
	// worker cache name.
	assetVersion string
	importMap    template.HTML
	// cspHead/cspTail surround form-action's SSO origin, which is added per
	// request so a swapped SSO address is honoured at once.
	cspHead, cspTail string

	tmplMu sync.Mutex
	tmpls  map[string]*template.Template

	siteName       atomic.Value
	publicRequests atomic.Bool

	sso atomic.Pointer[sso.Client]
	// ssoBroken is why saved SSO settings were refused ("" when fine);
	// ssoBaseURL is this app's configured public address.
	ssoBroken  atomic.Value
	ssoBaseURL atomic.Value
	grants     *grantChecks

	limiter *loginLimiter
	// requestLimiter counts room requests sent by visitors, per IP.
	requestLimiter *loginLimiter
	handler        http.Handler
}

func New(cfg Config, st *store.Store, logger *slog.Logger) (*Server, error) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	if cfg.Env == nil {
		cfg.Env = os.Getenv
	}
	s := &Server{cfg: cfg, store: st, log: logger, limiter: newLoginLimiter(10, 15*time.Minute),
		requestLimiter: newLoginLimiter(5, time.Hour), grants: newGrantChecks()}
	s.ssoBroken.Store("")
	s.ssoBaseURL.Store("")
	if cfg.Dev {
		s.webFS = os.DirFS("web")
	} else {
		s.webFS = web.FS
	}
	var err error
	if s.static, err = fs.Sub(s.webFS, "static"); err != nil {
		return nil, err
	}
	if err := s.computeAssets(); err != nil {
		return nil, err
	}
	// Secrets saved as plain text (by hand, or before sealing) are sealed now.
	if err := st.SealPlainSecrets(append(connections.SecretKeys(connectionDefs), pendingKey("sso_client_secret"))...); err != nil {
		return nil, err
	}
	if err := s.reloadSSO(); err != nil {
		return nil, err
	}
	s.reloadSettings()
	for _, d := range connectionDefs {
		if r := s.connection(d.ID); r.FromEnv && !r.On() {
			s.log.Warn("connection set on the server but incomplete, so it's off", "connection", d.ID)
		}
	}
	if !cfg.Dev {
		if err := s.parseTemplates(); err != nil {
			return nil, err
		}
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) reloadSettings() {
	s.siteName.Store(s.store.Setting("site_name", "Church Calendar"))
	// Never saved: on (visitors may ask). An admin who saved "off" keeps it.
	s.publicRequests.Store(s.store.Setting(publicRequestsSetting, "1") == "1")
}

func (s *Server) SiteName() string { return s.siteName.Load().(string) }

// buildSSO checks the SSO connection's values (by settings key) and makes
// the client plus the configured base URL. With no SSO address the client
// is disabled and the app uses its local login.
func buildSSO(v map[string]string) (*sso.Client, string, error) {
	off := sso.New("", "", "", "")
	base := strings.TrimRight(strings.TrimSpace(v["base_url"]), "/")
	if base != "" {
		if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return off, "", fmt.Errorf("this app's address %q must be an http(s) address like https://calendar.example.org", base)
		}
	}
	c := sso.New(v["sso_url"], v["sso_internal_url"], v["sso_client_id"], v["sso_client_secret"])
	if !c.Enabled() {
		return c, base, nil
	}
	for name, a := range map[string]string{"the User Management address": c.PublicURL, "the internal address": c.InternalURL} {
		if u, err := url.Parse(a); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return off, "", fmt.Errorf("%s %q must be an http(s) address", name, a)
		}
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return off, "", errors.New("a User Management address is set, so the client ID and client secret are needed too (from registering this app in User Management)")
	}
	return c, base, nil
}

// reloadSSO rebuilds the SSO client from the connection's live values
// (never the sso_pending_* ones, kept for the sign-in test). Wrong values
// from the environment are an error (a deploy mistake, loud at start);
// wrong saved ones never stop the app: it logs, remembers why for the
// admin banner and uses local login.
func (s *Server) reloadSSO() error {
	r := s.connection("sso")
	vals, err := r.Values, error(nil)
	switch {
	case r.FromEnv:
		vals = r.FromServer
	case len(r.Unreadable) > 0:
		err = errors.New("the saved client secret can't be read (the database came from a server with another secrets.key)")
	}
	c, base := sso.New("", "", "", ""), ""
	if err == nil {
		c, base, err = buildSSO(vals)
	}
	broken := ""
	if err != nil {
		if r.FromEnv {
			return fmt.Errorf("single sign-on (environment): %w", err)
		}
		broken = err.Error()
		s.log.Error("saved single sign-on settings are broken; using local login", "err", err)
	}
	s.sso.Store(c)
	s.ssoBaseURL.Store(base)
	s.ssoBroken.Store(broken)
	if c.Enabled() && s.store.Setting(localLoginSetting, "") == "on" {
		s.log.Warn("local login is on (break-glass): /login shows the password form; turn it off with the local-login off command")
	}
	return nil
}

// SSO is the current sign-in client; it may be swapped between calls, so
// read it once where consistency matters.
func (s *Server) SSO() *sso.Client { return s.sso.Load() }

// configuredBaseURL is this app's public address from the SSO connection
// ("" if unset).
func (s *Server) configuredBaseURL() string { return s.ssoBaseURL.Load().(string) }

// SSOBroken is why saved SSO settings were refused, or "".
func (s *Server) SSOBroken() string { return s.ssoBroken.Load().(string) }

// computeAssets hashes the static tree and builds the import map. The
// import map is an inline script, so its hash goes into the CSP.
func (s *Server) computeAssets() error {
	h := sha256.New()
	var files []string
	err := fs.WalkDir(s.static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := fs.ReadFile(s.static, f)
		if err != nil {
			return err
		}
		h.Write([]byte(f))
		h.Write(b)
	}
	s.assetVersion = hex.EncodeToString(h.Sum(nil))[:12]

	im, err := json.MarshalIndent(map[string]any{
		"imports": map[string]string{
			"@hotwired/stimulus":  s.asset("js/vendor/stimulus.js"),
			"stimulus-autoloader": s.asset("js/stimulus_autoloader.js"),
		},
	}, "", "  ")
	if err != nil {
		return err
	}
	// Emitted verbatim as a whole tag: html/template would otherwise
	// JS-escape it inside the <script> element.
	s.importMap = template.HTML(`<script type="importmap">` + string(im) + `</script>`)
	sum := sha256.Sum256(im)
	s.cspHead = strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		"style-src 'self'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"manifest-src 'self'",
		"worker-src 'self'",
		"base-uri 'self'",
		// A form posted after a sign-in ends is redirected to /login and
		// on to User Management; browsers check form-action on redirects.
		"form-action 'self'",
	}, "; ")
	s.cspTail = "; frame-ancestors 'none'"
	return nil
}

// contentSecurityPolicy is the policy for one response, with the current
// SSO origin allowed in form-action.
//
// The Connections pages also allow the pending SSO origin, because the
// Test sign-in form there redirects to it (browsers check form-action on
// redirects).
func (s *Server) contentSecurityPolicy(r *http.Request) string {
	extra := ""
	if strings.HasPrefix(r.URL.Path, "/admin/connections") {
		if o := s.pendingOrigin(); o != s.ssoOrigin() {
			extra = o
		}
	}
	return s.cspHead + s.ssoOrigin() + extra + s.cspTail
}

// ssoOrigin is " <User Management's origin>" for the CSP, or "".
func (s *Server) ssoOrigin() string {
	c := s.SSO()
	if !c.Enabled() {
		return ""
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil {
		return ""
	}
	return " " + u.Scheme + "://" + u.Host
}

// asset returns the versioned URL for a file under web/static.
func (s *Server) asset(p string) string {
	return "/static/" + strings.TrimPrefix(p, "/") + "?v=" + s.assetVersion
}

// Templates --------------------------------------------------------------

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"asset":              s.asset,
		"relDays":            s.relDays,
		"fmtDate":            fmtDate,
		"fmtTime":            fmtTime,
		"today":              func() string { return s.today() },
		"dict":               dict,
		"roleLabel":          store.RoleLabel,
		"pair":               func(a, b store.Booking) []store.Booking { return []store.Booking{a, b} },
		"canSee":             func(b store.Booking, u *store.User) bool { return canSeeDetails(&b, u) },
		"source":             sourceLabel,
		"bookingStatusLabel": store.BookingStatusLabel,
		"requestStatusLabel": store.RequestStatusLabel,
		"add":                func(a, b int) int { return a + b },
		"pluralize":          pluralize,
		"join":               strings.Join,
		"lines":              func(t string) []string { return strings.Split(strings.TrimSpace(t), "\n") },
		// tel: links aren't on html/template's safe list; the number is
		// only ever digits and a leading + here.
		"telURL": func(phone string) template.URL {
			if d := phoneDigits(phone); d != "" {
				if strings.HasPrefix(strings.TrimSpace(phone), "+") {
					d = "+" + d
				}
				return template.URL("tel:" + d)
			}
			return ""
		},
	}
}

func (s *Server) parseTemplates() error {
	pages, err := fs.Glob(s.webFS, "templates/pages/*.html")
	if err != nil {
		return err
	}
	sub, _ := fs.Glob(s.webFS, "templates/pages/*/*.html")
	pages = append(pages, sub...)
	shared := []string{"templates/layout.html", "templates/partials/*.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/pages/"), ".html")
		t, err := template.New("layout.html").Funcs(s.funcs()).ParseFS(s.webFS, append(shared, p)...)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		tmpls[name] = t
	}
	s.tmplMu.Lock()
	s.tmpls = tmpls
	s.tmplMu.Unlock()
	return nil
}

func (s *Server) template(name string) (*template.Template, error) {
	if s.cfg.Dev {
		if err := s.parseTemplates(); err != nil {
			return nil, err
		}
	}
	s.tmplMu.Lock()
	defer s.tmplMu.Unlock()
	t, ok := s.tmpls[name]
	if !ok {
		return nil, fmt.Errorf("no template %q", name)
	}
	return t, nil
}

type flash struct {
	Kind    string // "success" | "error" | "info"
	Message string
}

// render executes a page inside the layout. Every page gets the common
// keys (User, CSRF, Flash, SiteName, ...) merged into data.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["User"] = currentUser(r)
	data["CSRF"] = csrfToken(r)
	data["SiteName"] = s.SiteName()
	if u := currentUser(r); u.IsAdmin() {
		data["SSOBroken"] = s.SSOBroken()
	}
	data["ImportMap"] = s.importMap
	data["AssetVersion"] = s.assetVersion
	data["Path"] = r.URL.Path
	data["URI"] = r.URL.RequestURI()
	if f := s.popFlash(w, r); f != nil {
		data["Flash"] = f
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = ""
	}
	t, err := s.template(page)
	if err != nil {
		s.log.Error("template lookup", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	// Render into a buffer so a template error doesn't produce half a page.
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.log.Error("template exec", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(buf.String()))
}

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, kind, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: "cal_flash", Value: base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + msg)),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r), MaxAge: 60,
	})
}

func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie("cal_flash")
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: "cal_flash", Path: "/", MaxAge: -1})
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	kind, msg, ok := strings.Cut(string(b), "|")
	if !ok {
		return nil
	}
	return &flash{Kind: kind, Message: msg}
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, flashMsg string) {
	if flashMsg != "" {
		s.setFlash(w, r, "success", flashMsg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// Errors -----------------------------------------------------------------

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	s.render(w, r, http.StatusInternalServerError, "error", map[string]any{
		"Title": "Something went wrong", "Message": "An unexpected error occurred. It has been logged.",
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", map[string]any{
		"Title": "Not found", "Message": "We couldn't find what you were looking for.",
	})
}

// Dates ------------------------------------------------------------------

// today is the current date in the server's local time zone (set TZ).
func (s *Server) today() string { return time.Now().Format(store.DateLayout) }

func (s *Server) todayTime() time.Time {
	t, _ := time.Parse(store.DateLayout, s.today())
	return t
}

func (s *Server) relDays(date string) string {
	d, err := time.Parse(store.DateLayout, date)
	if err != nil {
		return ""
	}
	n := int(d.Sub(s.todayTime()).Hours() / 24)
	switch {
	case n == 0:
		return "today"
	case n == 1:
		return "tomorrow"
	case n == -1:
		return "yesterday"
	case n > 1:
		return "in " + humanDays(n)
	default:
		return humanDays(-n) + " ago"
	}
}

func humanDays(n int) string {
	switch {
	case n < 14:
		return pluralize(n, "day")
	case n < 60:
		return pluralize(n/7, "week")
	case n < 730:
		return pluralize(n/30, "month")
	default:
		return pluralize(n/365, "year")
	}
}

func fmtDate(date string) string {
	d, err := time.Parse(store.DateLayout, date)
	if err != nil {
		return date
	}
	return d.Format("Jan 2, 2006")
}

// fmtTime renders a SQLite UTC timestamp ("YYYY-MM-DD HH:MM:SS") in local time.
func fmtTime(ts string) string {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		return ts
	}
	return t.Local().Format("Jan 2, 2006 3:04 PM")
}

func pluralize(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict needs key/value pairs")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, errors.New("dict keys must be strings")
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// Form helpers -----------------------------------------------------------

func formStr(r *http.Request, key string) string { return strings.TrimSpace(r.PostFormValue(key)) }

func formInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue(key)), 10, 64)
	return n
}

func queryInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}

func pathID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

func validDate(d string) bool {
	_, err := time.Parse(store.DateLayout, d)
	return err == nil
}

func validTime(t string) bool {
	_, err := time.Parse("15:04", t)
	return err == nil
}

// Context ----------------------------------------------------------------

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
	ctxCSRF
)

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

func csrfToken(r *http.Request) string {
	t, _ := r.Context().Value(ctxCSRF).(string)
	return t
}

func withValue(r *http.Request, k ctxKey, v any) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), k, v))
}

// safeRedirect only allows local paths, preventing open redirects.
func safeRedirect(to string) string {
	if to == "" || !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.HasPrefix(to, "/\\") {
		return "/"
	}
	return path.Clean(to)
}
