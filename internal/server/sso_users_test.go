package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/sso"
	"github.com/tylergoza/church-calendar/internal/store"
)

// people is the calendar's list in the fake User Management: alice (who
// signs in, admin), bob (scheduler), carol (locked) and dave (account off).
func people() sso.AppUsers {
	return sso.AppUsers{
		Roles: []string{"member", "scheduler", "admin"},
		Users: []sso.AppUser{
			{User: sso.User{Sub: "42", Username: "alice", Name: "Alice Admin", Role: "admin"}, Active: true},
			{User: sso.User{Sub: "43", Username: "bob", Name: "Bob B", Role: "scheduler"}, Active: true,
				UpdatedBy: "Sam", UpdatedAt: "2026-01-02 15:04:05"},
			{User: sso.User{Sub: "44", Username: "carol", Name: "Carol C", Role: "scheduler"}, Active: true, Locked: true},
			{User: sso.User{Sub: "45", Username: "dave", Name: "Dave D", Role: "scheduler"}, Disabled: true},
		},
	}
}

// ssoAdmin starts a calendar with SSO and signs alice (admin) in.
func ssoAdmin(t *testing.T, setup func(f *fakeUM)) (*fakeUM, *client, *store.Store) {
	t.Helper()
	f := newFakeUM(t)
	f.set(func(f *fakeUM) {
		f.appUsers = people()
		if setup != nil {
			setup(f)
		}
	})
	c, st := newTestServerWith(t, ssoConfig(f))
	c.get("/login", 200)
	return f, c, st
}

// rowFor is the table row on the Users page for username.
func rowFor(t *testing.T, body, username string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<tr>\s*<td data-label="Username"><strong>` + username + `</strong>.*?</tr>`).FindString(body)
	if m == "" {
		t.Fatalf("no row for %s:\n%s", username, body)
	}
	return m
}

func TestSSOUsersPageLists(t *testing.T) {
	_, c, _ := ssoAdmin(t, nil)
	body := c.get("/admin/users", 200)
	for _, want := range []string{"Bob B", "Updated by Sam", "Ask a user admin", `<option value="scheduler" selected>`} {
		if !strings.Contains(body, want) {
			t.Errorf("Users page missing %q", want)
		}
	}
	if strings.Contains(body, "Manage in User Management") || strings.Contains(body, "/admin/users/new") {
		t.Error("expected no Manage link and no Add user")
	}
	if bob := rowFor(t, body, "bob"); !strings.Contains(bob, `action="/admin/access/43"`) || !strings.Contains(bob, `name="suspended" value="1"`) {
		t.Errorf("bob's row should have the role and suspend forms:\n%s", bob)
	}
	carol := rowFor(t, body, "carol")
	if strings.Contains(carol, "<form") || !strings.Contains(carol, "Locked by a user admin in User Management") {
		t.Errorf("locked row should be read-only with a note:\n%s", carol)
	}
	if dave := rowFor(t, body, "dave"); strings.Contains(dave, "<form") || !strings.Contains(dave, "Account turned off") {
		t.Errorf("turned-off row should be read-only:\n%s", dave)
	}
	// The local add/edit/delete pages are gone.
	c.get("/admin/users/new", 404)
	c.get("/admin/users/1/edit", 404)
}

func TestSSOUsersPageUserAdminLink(t *testing.T) {
	f, c, _ := ssoAdmin(t, func(f *fakeUM) { f.appUsers.Users[0].UserAdmin = true })
	body := c.get("/admin/users", 200)
	if !strings.Contains(body, `href="`+f.srv.URL+`/admin/users"`) || strings.Contains(body, "Ask a user admin") {
		t.Fatal("a user admin should get the Manage in User Management link instead of the note")
	}
}

func TestSSOUsersRoleChange(t *testing.T) {
	f, c, st := ssoAdmin(t, nil)
	body := c.post("/admin/users", "/admin/access/43", url.Values{"role": {"admin"}}, 200)
	if !strings.Contains(body, "Bob B is now Administrator.") {
		t.Fatalf("expected the success flash:\n%s", body)
	}
	if len(f.patches) != 1 {
		t.Fatalf("%d PATCHes, want 1", len(f.patches))
	}
	p := f.patches[0]
	if p.sub != "43" || p.actingGrant != testGrant || len(p.body) != 1 || p.body["role"] != "admin" {
		t.Fatalf("bad PATCH: %+v", p)
	}
	if bob, err := st.GetUserBySubject("43"); err != nil || !bob.IsAdmin() || !bob.Active {
		t.Fatalf("local user not updated: %+v %v", bob, err)
	}
	// The list is read again, not the cached one.
	if !strings.Contains(rowFor(t, body, "bob"), `<option value="admin" selected>`) {
		t.Fatal("page still shows the old role")
	}
}

func TestSSOUsersRefusalShowsMessage(t *testing.T) {
	f, c, _ := ssoAdmin(t, func(f *fakeUM) { f.patchErr = "last_admin" })
	body := c.post("/admin/users", "/admin/access/42", url.Values{"role": {"scheduler"}}, 200)
	if !strings.Contains(body, "This app needs at least one admin; make someone else an admin first.") {
		t.Fatalf("expected the last_admin message:\n%s", body)
	}
	if len(f.patches) != 1 {
		t.Fatalf("%d PATCHes, want 1", len(f.patches))
	}
}

func TestSSOUsersSuspendSignsOut(t *testing.T) {
	f, c, st := ssoAdmin(t, nil)
	// bob signs in too.
	f.set(func(f *fakeUM) { f.user = sso.User{Sub: "43", Username: "bob", Name: "Bob B", Role: "scheduler"} })
	bob := &client{t: t, base: c.base, http: &http.Client{Jar: mustJar()}}
	bob.get("/login", 200)
	bobToken := bob.sessionToken()
	f.set(func(f *fakeUM) { f.user = sso.User{Sub: "42", Username: "alice", Name: "Alice Admin", Role: "admin"} })

	body := c.post("/admin/users", "/admin/access/43", url.Values{"suspended": {"1"}}, 200)
	if !strings.Contains(body, "Bob B is suspended") {
		t.Fatalf("expected the suspended flash:\n%s", body)
	}
	if p := f.patches[0]; p.body["suspended"] != true || p.actingGrant != testGrant {
		t.Fatalf("bad PATCH: %+v", p)
	}
	if sessionExists(t, st, bobToken) {
		t.Fatal("bob's session survived the suspension")
	}
	if u, _ := st.GetUserBySubject("43"); u.Active {
		t.Fatal("bob still active locally")
	}
	if !strings.Contains(rowFor(t, body, "bob"), `name="suspended" value="0"`) {
		t.Fatal("expected a Restore button for bob")
	}

	// Restoring makes him active again.
	c.post("/admin/users", "/admin/access/43", url.Values{"suspended": {"0"}}, 200)
	if u, _ := st.GetUserBySubject("43"); !u.Active {
		t.Fatal("bob not active again after restore")
	}
}

// Opening the Users page brings the local cache in line with User
// Management: people it no longer lists are marked inactive here.
func TestSSOUsersSyncMarksMissingInactive(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	// Cached from earlier: bob and erin. User Management now lists alice
	// and bob only.
	for _, u := range []struct{ sub, name string }{{"43", "bob"}, {"46", "erin"}} {
		if _, err := st.UpsertSSOUser(u.sub, u.name, "", store.RoleMember); err != nil {
			t.Fatal(err)
		}
	}
	f.set(func(f *fakeUM) {
		f.appUsers = people()
		f.appUsers.Users = f.appUsers.Users[:2]
	})
	c.get("/login", 200)
	c.get("/admin/users", 200)
	if u, _ := st.GetUserBySubject("46"); u.Active {
		t.Fatal("erin should be marked inactive by the sync")
	}
	if u, _ := st.GetUserBySubject("43"); !u.Active {
		t.Fatal("bob should still be active")
	}
}

func TestSSOUsersBreakGlassReadOnly(t *testing.T) {
	f, c, st := ssoAdmin(t, nil)
	u, _ := st.GetUserBySubject("42")
	if err := st.SetSetting(localLoginSetting, "on"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(u.ID, "a long password"); err != nil {
		t.Fatal(err)
	}
	local := &client{t: t, base: c.base, http: &http.Client{Jar: mustJar()}}
	local.post("/login", "/login", url.Values{"username": {"alice"}, "password": {"a long password"}}, 200)

	body := local.get("/admin/users", 200)
	if !strings.Contains(body, "this list is read-only") || strings.Contains(body, `action="/admin/access/`) {
		t.Fatalf("break-glass admin should see the list read-only:\n%s", body)
	}
	if !strings.Contains(body, "Bob B") {
		t.Fatal("the list should still show")
	}
	body = local.post("/admin/users", "/admin/access/43", url.Values{"role": {"admin"}}, 200)
	if !strings.Contains(body, "signed in with the local login") || len(f.patches) != 0 {
		t.Fatalf("break-glass change should be refused here (%d PATCHes):\n%s", len(f.patches), body)
	}
}

func TestSSOOffUsersPageUnchanged(t *testing.T) {
	c, st := newTestServer(t)
	if _, err := st.CreateUser("alice", "", "a long password", store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser("bob", "Bob B", "a long password", store.RoleMember); err != nil {
		t.Fatal(err)
	}
	c.post("/login", "/login", url.Values{"username": {"alice"}, "password": {"a long password"}}, 200)
	body := c.get("/admin/users", 200)
	if !strings.Contains(body, `href="/admin/users/new"`) || !strings.Contains(body, "/admin/users/2/edit") || strings.Contains(body, "/admin/access/") {
		t.Fatalf("expected the old Users page:\n%s", body)
	}
	c.get("/admin/users/new", 200)
	c.post("/admin/users", "/admin/access/43", url.Values{"role": {"admin"}}, 404)
}

func TestSSOOffLastAdminGuard(t *testing.T) {
	c, st := newTestServer(t)
	alice, err := st.CreateUser("alice", "", "a long password", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.CreateUser("old", "Old Admin", "a long password", store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetActive(old, false); err != nil {
		t.Fatal(err)
	}
	c.post("/login", "/login", url.Values{"username": {"alice"}, "password": {"a long password"}}, 200)

	body := c.post("/admin/users/"+strconv.FormatInt(old, 10)+"/edit", "/admin/users/"+strconv.FormatInt(old, 10), url.Values{"display_name": {"Old Admin"}}, 200)
	if !strings.Contains(body, "User old updated.") {
		t.Fatalf("demoting an inactive admin should work:\n%s", body)
	}
	if u, _ := st.GetUser(old); u.IsAdmin() {
		t.Fatal("old is still an admin")
	}
	body = c.post("/admin/users/"+strconv.FormatInt(alice, 10)+"/edit", "/admin/users/"+strconv.FormatInt(alice, 10), url.Values{"display_name": {"Alice"}}, 422)
	if !strings.Contains(body, "At least one administrator is required.") {
		t.Fatalf("the last active admin shouldn't be able to demote themselves:\n%s", body)
	}
	if u, _ := st.GetUser(alice); !u.IsAdmin() {
		t.Fatal("alice lost admin")
	}
}
