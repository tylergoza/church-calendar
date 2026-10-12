package server

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newTestServer(t *testing.T) (*client, *store.Store) {
	t.Helper()
	return newTestServerWith(t, Config{})
}

func newTestServerWith(t *testing.T, cfg Config) (*client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return startServer(t, cfg, st), st
}

// startServer runs a server on an existing store. Tests never read the
// real environment.
func startServer(t *testing.T, cfg Config, st *store.Store) *client {
	t.Helper()
	if cfg.Env == nil {
		cfg.Env = func(string) string { return "" }
	}
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
}

func (c *client) get(path string, wantStatus int) string {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

var csrfRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// post submits a form, pulling the CSRF token from a page first.
func (c *client) post(tokenPage, path string, form url.Values, wantStatus int) string {
	c.t.Helper()
	m := csrfRe.FindStringSubmatch(c.get(tokenPage, 200))
	if m == nil {
		c.t.Fatalf("no csrf token on %s", tokenPage)
	}
	form.Set("_csrf", m[1])
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("POST %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// as signs in as someone else in a browser of their own.
func (c *client) as(username string) *client {
	jar, _ := cookiejar.New(nil)
	other := &client{t: c.t, base: c.base, http: &http.Client{Jar: jar}}
	other.post("/login", "/login", url.Values{"username": {username}, "password": {"a long password"}}, 200)
	return other
}

func TestSetupLoginLogout(t *testing.T) {
	c, st := newTestServer(t)

	// No users yet: sign in leads to setup.
	if body := c.get("/login", 200); !strings.Contains(body, "Create the first administrator") {
		t.Fatal("expected setup page")
	}
	if body := c.post("/setup", "/setup", url.Values{
		"site_name": {"Grace Calendar"}, "username": {"pat"}, "password": {"short"}, "password_confirm": {"short"},
	}, 422); !strings.Contains(body, "at least 10 characters") {
		t.Error("expected a password error")
	}
	body := c.post("/setup", "/setup", url.Values{
		"site_name": {"Grace Calendar"}, "username": {"pat"}, "display_name": {"Pat"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)
	if !strings.Contains(body, "Grace Calendar") || !strings.Contains(body, "administrator account is ready") {
		t.Error("expected to land signed in with the new site name")
	}
	if u, _ := st.GetUser(1); !u.IsAdmin() {
		t.Error("the first user should be an admin")
	}
	// Setup is closed once someone exists.
	c.get("/setup", 200) // redirected to /login, which redirects home when signed in
	if !strings.Contains(c.get("/admin/settings", 200), "Grace Calendar") {
		t.Error("settings should show the site name")
	}

	c.post("/", "/logout", url.Values{}, 200)
	if strings.Contains(c.get("/", 200), "Sign out") {
		t.Error("still signed in after signing out")
	}
	if body := c.post("/login", "/login", url.Values{"username": {"pat"}, "password": {"wrong password"}}, 401); !strings.Contains(body, "Incorrect") {
		t.Error("expected a sign-in error")
	}
	c.as("pat")
}

func TestRolesGateAdminPages(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("sam", "", "a long password", store.RoleScheduler)
	st.CreateUser("mel", "", "a long password", store.RoleMember)

	admin := []string{"/admin/users", "/admin/users/new", "/admin/settings", "/admin/connections"}
	for _, who := range []string{"sam", "mel"} {
		u := c.as(who)
		for _, p := range admin {
			u.get(p, 403)
		}
		u.post("/account", "/admin/connections/events", url.Values{"events_url": {"https://x.example"}}, 403)
		if strings.Contains(u.get("/", 200), "/admin/connections") {
			t.Errorf("%s sees the Connections link", who)
		}
	}
	boss := c.as("boss")
	for _, p := range admin {
		boss.get(p, 200)
	}
	// Signed out: admin pages go to sign in.
	if body := c.get("/admin/connections", 200); !strings.Contains(body, `action="/login"`) {
		t.Error("expected the sign-in form")
	}
}

func TestSecurityHeadersAndPWA(t *testing.T) {
	c, _ := newTestServer(t)
	resp, err := c.http.Get(c.base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
	for _, ck := range resp.Cookies() {
		if !strings.HasPrefix(ck.Name, "cal_") {
			t.Errorf("cookie %q should start cal_", ck.Name)
		}
	}
	if body := c.get("/manifest.webmanifest", 200); !strings.Contains(body, `"start_url":"/"`) {
		t.Error("manifest")
	}
	if body := c.get("/sw.js", 200); !strings.Contains(body, "cal-static-") || strings.Contains(body, "__VERSION__") {
		t.Error("service worker")
	}
	c.get("/offline", 200)
	if c.get("/healthz", 200) != "ok" {
		t.Error("healthz")
	}
	c.get("/static/js/application.js", 200)
	c.get("/nope", 404)
}

// No inline style attributes: the CSP is style-src 'self'.
func TestNoInlineStyles(t *testing.T) {
	err := filepath.WalkDir("../../web/templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		b, err := os.ReadFile(p)
		if strings.Contains(string(b), "style=") {
			t.Errorf("%s has an inline style attribute", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
