package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// newSSOConnServer is a running server with a boss (admin) and a hana
// (scheduler, who is not an admin).
func newSSOConnServer(t *testing.T, env func(string) string) (*Server, *store.Store, *client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("hana", "", "a long password", store.RoleScheduler)
	return startSSOConn(t, st, env)
}

func startSSOConn(t *testing.T, st *store.Store, env func(string) string) (*Server, *store.Store, *client) {
	t.Helper()
	srv, err := New(Config{Env: envOr(env)}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return srv, st, &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
}

// envOr is env, or an empty environment when nil (tests never read the real one).
func envOr(env func(string) string) func(string) string {
	if env == nil {
		return envOf(nil)
	}
	return env
}

func TestSavedSSOTurnsOnAfterReload(t *testing.T) {
	f := newFakeUM(t)
	srv, st, c := newSSOConnServer(t, nil)
	if srv.SSO().Enabled() {
		t.Fatal("SSO on with nothing set")
	}
	st.SetSetting("sso_url", f.srv.URL)
	st.SetSetting("sso_client_id", "calendar")
	st.SetSecretSetting("sso_client_secret", "s3cret")
	srv.reloadSSO()
	if !srv.SSO().Enabled() || srv.SSOBroken() != "" {
		t.Fatalf("enabled=%v broken=%q", srv.SSO().Enabled(), srv.SSOBroken())
	}
	resp := c.getRaw(c.noFollow(), "/login")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), f.srv.URL) {
		t.Fatalf("login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+f.srv.URL) {
		t.Fatalf("CSP not updated: %s", csp)
	}
	// Clearing it swaps back.
	st.SetSetting("sso_url", "")
	srv.reloadSSO()
	if srv.SSO().Enabled() {
		t.Fatal("SSO still on")
	}
	if csp := c.getRaw(c.noFollow(), "/login").Header.Get("Content-Security-Policy"); strings.Contains(csp, f.srv.URL) {
		t.Fatalf("CSP kept old origin: %s", csp)
	}
}

func TestPendingSSOIsNeverLive(t *testing.T) {
	srv, st, _ := newSSOConnServer(t, nil)
	st.SetSetting("sso_pending_url", "https://accounts.example.org")
	srv.reloadSSO()
	if srv.SSO().Enabled() {
		t.Fatal("pending values went live")
	}
}

// A saved connection missing a required value (here the secret) isn't
// broken, it's just off: the Connections model needs every required field.
func TestIncompleteSavedSSOIsOff(t *testing.T) {
	srv, st, c := newSSOConnServer(t, nil)
	st.SetSetting("sso_url", "https://accounts.example.org")
	st.SetSetting("sso_client_id", "calendar") // no secret
	srv.reloadSSO()
	if srv.SSO().Enabled() || srv.SSOBroken() != "" {
		t.Fatalf("enabled=%v broken=%q", srv.SSO().Enabled(), srv.SSOBroken())
	}
	if body := c.get("/login", 200); !strings.Contains(body, `name="password"`) {
		t.Fatal("no local login form")
	}
}

func TestBrokenSavedSSOFallsBack(t *testing.T) {
	srv, st, c := newSSOConnServer(t, nil)
	st.SetSetting("sso_url", "ftp://accounts.example.org")
	st.SetSetting("sso_client_id", "calendar")
	st.SetSecretSetting("sso_client_secret", "s3cret")
	srv.reloadSSO()
	if srv.SSO().Enabled() || srv.SSOBroken() == "" {
		t.Fatalf("enabled=%v broken=%q", srv.SSO().Enabled(), srv.SSOBroken())
	}
	if body := c.get("/login", 200); !strings.Contains(body, `name="password"`) {
		t.Fatal("no local login form")
	}
	boss := c.as("boss")
	if body := boss.get("/", 200); !strings.Contains(body, "Single sign-on settings are broken") {
		t.Fatal("admin sees no banner")
	}
	hana := c.as("hana")
	if body := hana.get("/", 200); strings.Contains(body, "Single sign-on settings are broken") {
		t.Fatal("scheduler sees the banner")
	}
	// Fixing it clears the banner.
	st.SetSetting("sso_url", "https://accounts.example.org")
	srv.reloadSSO()
	if srv.SSOBroken() != "" || !srv.SSO().Enabled() {
		t.Fatalf("not fixed: %q", srv.SSOBroken())
	}
}

func TestBrokenSavedSSOAtStart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetSetting("sso_url", "not a url")
	st.SetSetting("sso_client_id", "calendar")
	st.SetSecretSetting("sso_client_secret", "x")
	srv, err := New(Config{Env: envOf(nil)}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("saved settings stopped the app: %v", err)
	}
	if srv.SSOBroken() == "" || srv.SSO().Enabled() {
		t.Fatal("not marked broken")
	}
}

func TestBrokenEnvSSOStillFails(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, err = New(Config{Env: envOf(map[string]string{"SSO_URL": "https://accounts.example.org"})}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("env SSO without a client should fail")
	}
}
