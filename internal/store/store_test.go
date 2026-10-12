package store

import (
	"path/filepath"
	"testing"

	"github.com/tylergoza/church-calendar/internal/secrets"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrationsReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSetting("site_name", "Grace")
	st.Close()
	// Opening again runs nothing twice and keeps the data and the key.
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Setting("site_name", "") != "Grace" {
		t.Error("setting lost on reopen")
	}
}

func TestRoles(t *testing.T) {
	cases := map[string]string{"admin": RoleAdmin, "scheduler": RoleScheduler, "member": RoleMember, "": RoleMember, "user": RoleMember, "planner": RoleMember}
	for in, want := range cases {
		if got := NormalizeRole(in); got != want {
			t.Errorf("NormalizeRole(%q) = %q, want %q", in, got, want)
		}
	}
	if RoleFromSSO(" scheduler ") != RoleScheduler {
		t.Error("RoleFromSSO should trim")
	}
	member, sched, admin := &User{Role: RoleMember}, &User{Role: RoleScheduler}, &User{Role: RoleAdmin}
	if member.CanSchedule() || !sched.CanSchedule() || !admin.CanSchedule() {
		t.Error("CanSchedule")
	}
	if member.IsAdmin() || sched.IsAdmin() || !admin.IsAdmin() {
		t.Error("IsAdmin")
	}
	var nobody *User
	if nobody.CanSchedule() || nobody.IsAdmin() {
		t.Error("nil user has no access")
	}

	st := openTest(t)
	id, err := st.CreateUser("sam", "Sam", "a long password", RoleScheduler)
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := st.GetUser(id); u.Role != RoleScheduler {
		t.Errorf("role = %q", u.Role)
	}
	if _, err := st.DB.Exec(`UPDATE users SET role = 'planner' WHERE id = ?`, id); err == nil {
		t.Error("the database should refuse an unknown role")
	}
}

func TestSecretSettings(t *testing.T) {
	st := openTest(t)
	if err := st.SetSecretSetting("tracker_token", "mt_abc"); err != nil {
		t.Fatal(err)
	}
	if raw := st.Setting("tracker_token", ""); !secrets.IsSealed(raw) {
		t.Errorf("stored %q, want sealed", raw)
	}
	if v, err := st.SecretSetting("tracker_token"); err != nil || v != "mt_abc" {
		t.Errorf("SecretSetting = %q, %v", v, err)
	}
	st.SetSecretSetting("tracker_token", "")
	if v, _ := st.SecretSetting("tracker_token"); v != "" {
		t.Errorf("cleared secret = %q", v)
	}
	st.SetSetting("other_token", "plain")
	st.SealPlainSecrets("other_token", "missing")
	if raw := st.Setting("other_token", ""); !secrets.IsSealed(raw) {
		t.Error("plain secret not sealed")
	}
	if st.Setting("missing", "x") != "x" {
		t.Error("sealing should skip unset keys")
	}
}

// The schema's checks keep the booking rules the API relies on.
func TestBookingConstraints(t *testing.T) {
	st := openTest(t)
	exec := func(q string, args ...any) error { _, err := st.DB.Exec(q, args...); return err }

	if err := exec(`INSERT INTO bookings (source, starts_on) VALUES ('calendar', '2026-11-01')`); err != nil {
		t.Fatalf("hand-made booking: %v", err)
	}
	if err := exec(`INSERT INTO bookings (source, source_id, starts_on) VALUES ('events', '12', '2026-11-01')`); err != nil {
		t.Fatalf("pushed booking: %v", err)
	}
	if exec(`INSERT INTO bookings (source, source_id, starts_on) VALUES ('events', '12', '2026-11-02')`) == nil {
		t.Error("the same source and source_id twice should be refused")
	}
	if exec(`INSERT INTO bookings (source, starts_on) VALUES ('events', '2026-11-01')`) == nil {
		t.Error("an app's booking needs a source_id")
	}
	if exec(`INSERT INTO bookings (source, source_id, starts_on) VALUES ('calendar', '3', '2026-11-01')`) == nil {
		t.Error("a hand-made booking has no source_id")
	}
	if exec(`INSERT INTO bookings (source, starts_on, visibility) VALUES ('calendar', '2026-11-01', 'secret')`) == nil {
		t.Error("unknown visibility should be refused")
	}
	if exec(`INSERT INTO tokens (name, kind, hash, prefix) VALUES ('App', 'source', 'h1', 'cal_')`) == nil {
		t.Error("a source token needs its source")
	}
	if err := exec(`INSERT INTO tokens (name, kind, source, hash, prefix) VALUES ('Planner', 'source', 'events', 'h2', 'cal_')`); err != nil {
		t.Errorf("source token: %v", err)
	}
	if exec(`INSERT INTO conflict_ok (low_id, high_id) VALUES (2, 1)`) == nil {
		t.Error("conflict_ok pairs are stored low, high")
	}
	if err := exec(`INSERT INTO conflict_ok (low_id, high_id) VALUES (1, 2)`); err != nil {
		t.Errorf("conflict_ok: %v", err)
	}
	// Deleting a booking takes its "that's fine" marks with it.
	exec(`DELETE FROM bookings WHERE id = 2`)
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM conflict_ok`).Scan(&n)
	if n != 0 {
		t.Error("conflict_ok row should go with its booking")
	}
}
