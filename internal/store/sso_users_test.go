package store

import (
	"testing"
	"time"
)

func TestUpsertSSOUser(t *testing.T) {
	st := openTest(t)
	localID, _ := st.CreateUser("Tyler", "", "a long password", RoleAdmin)

	// The existing local user is linked by username, case-insensitively.
	u, err := st.UpsertSSOUser("sub-1", "tyler", "Tyler G", "member")
	if err != nil || u.ID != localID || u.SSOSubject != "sub-1" || u.Username != "tyler" || u.DisplayName != "Tyler G" || u.IsAdmin() || !u.Active {
		t.Fatalf("link: %+v %v", u, err)
	}
	// Found by subject from now on, even after a rename upstream.
	u, _ = st.UpsertSSOUser("sub-1", "tgoza", "Tyler G", "admin")
	if u.ID != localID || u.Username != "tgoza" || !u.IsAdmin() {
		t.Errorf("rename: %+v", u)
	}
	if got, err := st.GetUserBySubject("sub-1"); err != nil || got.ID != localID {
		t.Errorf("by subject: %+v %v", got, err)
	}
	if _, err := st.GetUserBySubject("nope"); err != ErrNotFound {
		t.Errorf("unknown subject: %v", err)
	}

	// A linked user is never taken over by a different subject with the
	// same username: the new person gets a new row and the old one is
	// moved aside.
	other, err := st.UpsertSSOUser("sub-2", "TGOZA", "Someone else", "member")
	if err != nil || other.ID == localID || other.SSOSubject != "sub-2" {
		t.Fatalf("collision: %+v %v", other, err)
	}
	old, _ := st.GetUser(localID)
	if old.SSOSubject != "sub-1" || old.Username == "tgoza" {
		t.Errorf("old row should keep its subject and lose the name: %+v", old)
	}
	// Its next sign-in, under a new name, puts it right.
	if u, _ = st.UpsertSSOUser("sub-1", "tyler", "Tyler G", "admin"); u.ID != localID || u.Username != "tyler" {
		t.Errorf("after collision: %+v", u)
	}

	// SSO users have no password, so no local login.
	if _, ok := st.Authenticate("TGOZA", ""); ok {
		t.Error("an SSO-only user signed in with no password")
	}
	if _, ok := st.Authenticate("tyler", "a long password"); !ok {
		t.Error("the linked user keeps their local password")
	}
	if _, err := st.UpsertSSOUser("", "x", "", "member"); err == nil {
		t.Error("a subject is required")
	}
}

func TestSyncSSOUsers(t *testing.T) {
	st := openTest(t)
	st.CreateUser("local", "", "a long password", RoleMember)
	ann, _ := st.UpsertSSOUser("a", "ann", "", "member")
	bob, _ := st.UpsertSSOUser("b", "bob", "", "member")
	bobSess, _ := st.CreateSSOSession(bob.ID, "grant-b", time.Hour)

	// Bob's access is removed, Cat is new, Dan is listed but suspended.
	err := st.SyncSSOUsers([]SSOUser{
		{Subject: "a", Username: "ann", DisplayName: "Ann", Active: true},
		{Subject: "c", Username: "cat", Active: true, Role: "admin"},
		{Subject: "d", Username: "dan", Active: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	users, _ := st.ListUsers()
	active := map[string]bool{}
	for _, u := range users {
		active[u.Username] = u.Active
	}
	want := map[string]bool{"local": true, "ann": true, "bob": false, "cat": true, "dan": false}
	if len(users) != len(want) {
		t.Fatalf("users: %+v", users)
	}
	for name, a := range want {
		if active[name] != a {
			t.Errorf("%s active = %v, want %v", name, active[name], a)
		}
	}
	if _, err := st.GetSession(bobSess.Token); err != ErrNotFound {
		t.Errorf("bob should be signed out: %v", err)
	}
	if _, ok := st.Authenticate("local", "a long password"); !ok {
		t.Error("unlinked local users are left alone")
	}

	// Back on the list: active again, history intact.
	st.SyncSSOUsers([]SSOUser{{Subject: "b", Username: "bob", Active: true}})
	if u, _ := st.GetUser(bob.ID); !u.Active {
		t.Error("bob should be active again")
	}
	if u, _ := st.GetUser(ann.ID); u.Active {
		t.Error("ann was left off the list")
	}
}

func TestSSOSessions(t *testing.T) {
	st := openTest(t)
	u, _ := st.UpsertSSOUser("a", "ann", "", "member")
	if _, err := st.CreateSSOSession(u.ID, "", time.Hour); err == nil {
		t.Error("an SSO session needs a grant")
	}
	sess, err := st.CreateSSOSession(u.ID, "grant-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(sess.Token)
	if err != nil || got.Grant != "grant-1" || got.GrantCheckedAt.IsZero() || got.User.SSOSubject != "a" {
		t.Fatalf("session: %+v %v", got, err)
	}
	st.DB.Exec(`UPDATE sessions SET grant_checked_at = '2000-01-01T00:00:00Z'`)
	if err := st.MarkGrantChecked(sess.Token); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetSession(sess.Token); time.Since(got.GrantCheckedAt) > time.Minute {
		t.Errorf("checked at: %v", got.GrantCheckedAt)
	}
	st.CreateSession(u.ID, time.Hour)
	if err := st.DeleteSessionsForUser(u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions left", n)
	}
}
