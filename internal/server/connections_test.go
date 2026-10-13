package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/secrets"
	"github.com/tylergoza/church-calendar/internal/store"
)

// fakeApp answers /healthz like one of the sibling apps.
func fakeApp(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			io.WriteString(w, "ok")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func adminClient(t *testing.T, cfg Config) (*client, *store.Store) {
	t.Helper()
	c, st := newTestServerWith(t, cfg)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	return c.as("boss"), st
}

func TestConnectionSaveAndTest(t *testing.T) {
	c, st := adminClient(t, Config{})
	app := fakeApp(t)
	page := "/admin/connections"

	if body := c.get(page, 200); strings.Count(body, "Not connected") != 3 {
		t.Error("all three connections should start off")
	}
	// Bad address: sent back with what was typed.
	if body := c.post(page, "/admin/connections/events", url.Values{"events_url": {"ftp://nope"}}, 422); !strings.Contains(body, "should look like") || !strings.Contains(body, "ftp://nope") {
		t.Error("expected an address error with the value kept")
	}
	// Saving tests the connection.
	if body := c.post(page, "/admin/connections/events", url.Values{"events_url": {app.URL + "/"}}, 200); !strings.Contains(body, "Connected to the event planner") {
		t.Errorf("expected the check to pass:\n%s", body)
	}
	if got := st.Setting("events_url", ""); got != app.URL {
		t.Errorf("events_url = %q, want %q (trailing slash trimmed)", got, app.URL)
	}
	if body := c.get(page, 200); !strings.Contains(body, "Set here") || !strings.Contains(body, "Last changed by boss") {
		t.Error("expected Set here and who changed it")
	}
	// A failing app still saves.
	if body := c.post(page, "/admin/connections/events", url.Values{"events_url": {"http://127.0.0.1:1"}}, 200); !strings.Contains(body, "can&#39;t reach") {
		t.Errorf("expected a can't reach message:\n%s", body)
	}
	if st.Setting("events_url", "") != "http://127.0.0.1:1" {
		t.Error("a failed test should still save")
	}
	c.post(page, "/admin/connections/nope", url.Values{}, 404)
}

func TestConnectionSecrets(t *testing.T) {
	c, st := adminClient(t, Config{})
	page := "/admin/connections"

	if body := c.post(page, "/admin/connections/tracker", url.Values{"tracker_url": {"maint.example.org"}, "tracker_token": {"nope"}}, 422); !strings.Contains(body, "start with mt_") {
		t.Error("expected a token prefix error")
	}
	c.post(page, "/admin/connections/tracker", url.Values{"tracker_url": {"maint.example.org"}, "tracker_token": {"mt_abcdefgh1234"}}, 200)
	if raw := st.Setting("tracker_token", ""); !secrets.IsSealed(raw) || strings.Contains(raw, "mt_") {
		t.Errorf("token stored as %q, want sealed", raw)
	}
	if st.Setting("tracker_url", "") != "https://maint.example.org" {
		t.Error("address should get https://")
	}
	body := c.get(page, 200)
	if strings.Contains(body, "mt_abcdefgh1234") || !strings.Contains(body, "ends …1234") {
		t.Error("the token must never be shown, only its last four characters")
	}
	// A blank token on save keeps the old one.
	c.post(page, "/admin/connections/tracker", url.Values{"tracker_url": {"https://maint2.example.org"}, "tracker_token": {""}}, 200)
	if v, _ := st.SecretSetting("tracker_token"); v != "mt_abcdefgh1234" {
		t.Errorf("token = %q after a blank save", v)
	}
	// Remove clears it, and the connection goes off.
	c.post(page, "/admin/connections/tracker", url.Values{"tracker_url": {"https://maint2.example.org"}, "remove_tracker_token": {"1"}}, 200)
	if v, _ := st.SecretSetting("tracker_token"); v != "" {
		t.Errorf("token = %q after remove", v)
	}
	if body := c.get(page, 200); !strings.Contains(body, "Not connected") {
		t.Error("tracker with no token should be off")
	}
	// Disconnect forgets the rest.
	c.post(page, "/admin/connections/tracker/clear", url.Values{}, 200)
	if st.Setting("tracker_url", "") != "" {
		t.Error("disconnect should clear the address")
	}
}

func TestConnectionFromEnvironment(t *testing.T) {
	env := map[string]string{"TRACKER_URL": "https://env.example.org", "TRACKER_TOKEN": "mt_fromtheserver9"}
	c, st := adminClient(t, Config{Env: func(k string) string { return env[k] }})
	page := "/admin/connections"
	st.SetSetting("tracker_url", "https://old.example.org")

	body := c.get(page, 200)
	if !strings.Contains(body, "Set on the server") || !strings.Contains(body, "https://env.example.org") || strings.Contains(body, "mt_fromtheserver9") {
		t.Error("expected the server's values, with the token hidden")
	}
	if !strings.Contains(body, "Different values are saved here") {
		t.Error("expected the settings-differ warning")
	}
	// Can't be changed here while the server sets it.
	if body := c.post(page, "/admin/connections/tracker", url.Values{"tracker_url": {"https://x.example.org"}}, 200); !strings.Contains(body, "set on the server") {
		t.Error("expected saving to be refused")
	}
	if st.Setting("tracker_url", "") != "https://old.example.org" {
		t.Error("settings changed while set on the server")
	}
	// Keep in settings copies them over, token sealed.
	c.post(page, "/admin/connections/tracker/keep", url.Values{}, 200)
	if v, _ := st.SecretSetting("tracker_token"); v != "mt_fromtheserver9" || !secrets.IsSealed(st.Setting("tracker_token", "")) {
		t.Error("keep in settings should copy the sealed token")
	}
	if body := c.get(page, 200); !strings.Contains(body, "safe to remove these variables") {
		t.Error("expected the settings-match note")
	}
}

// A database restored without its secrets.key: the connection says to
// enter the token again, and nothing else breaks.
func TestConnectionUnreadableSecret(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetSetting("tracker_url", "https://maint.example.org")
	st.SetSecretSetting("tracker_token", "mt_abcdefgh1234")
	st.Close()
	// A new key: as if the database moved to a server without it.
	if err := os.Remove(filepath.Join(dir, secrets.KeyFile)); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := startServer(t, Config{}, st)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	if body := c.as("boss").get("/admin/connections", 200); !strings.Contains(body, "Enter it again") {
		t.Error("expected the unreadable-token note")
	}
}

func TestPlainSecretsSealedOnStart(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SetSetting("tracker_token", "mt_plain")
	startServer(t, Config{}, st)
	if raw := st.Setting("tracker_token", ""); !secrets.IsSealed(raw) {
		t.Errorf("tracker_token = %q, want sealed on start", raw)
	}
	if v, _ := st.SecretSetting("tracker_token"); v != "mt_plain" {
		t.Errorf("value = %q", v)
	}
}

func TestClearConnection(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.SetSetting("events_url", "https://events.example.org")
	if err := ClearConnection(st, "events"); err != nil {
		t.Fatal(err)
	}
	if st.Setting("events_url", "") != "" {
		t.Error("clear left the address")
	}
	if err := ClearConnection(st, "nope"); err == nil {
		t.Error("expected an error for an unknown connection")
	}
}
