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

// TestMigration002 runs 001 → 002 on a database holding rows: every waiting
// request gets a pending booking in its rooms, and nothing else changes.
func TestMigration002(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := openAt(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username) VALUES (1, 'mel')`,
		`INSERT INTO rooms (id, name, needs_approval) VALUES (1, 'Hall', 1), (2, 'Kitchen', 0)`,
		`INSERT INTO bookings (id, source, title, starts_on, start_time, status) VALUES (1, 'calendar', 'Choir', '2026-11-07', '17:00', 'tentative')`,
		`INSERT INTO booking_rooms (booking_id, room_id, room_name) VALUES (1, 1, 'Hall')`,
		`INSERT INTO requests (id, code, name, contact, title, starts_on, start_time, end_time) VALUES (1, 'c1', 'Jo', 'jo@example.org', 'Movie night', '2026-11-08', '18:00', '21:00')`,
		`INSERT INTO requests (id, code, name, contact, user_id, title, starts_on) VALUES (2, 'c2', 'Mel', 'mel', 1, 'Prayer', '2026-11-09')`,
		`INSERT INTO requests (id, code, name, contact, title, starts_on, status) VALUES (3, 'c3', 'Al', '555', 'Old', '2026-01-01', 'declined')`,
		`INSERT INTO request_rooms (request_id, room_id) VALUES (1, 1), (1, 2), (2, 2), (3, 1)`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if b, err := st.GetBooking(1); err != nil || b.Title != "Choir" || b.Status != "tentative" || b.RoomNames() != "Hall" {
		t.Errorf("old booking = %+v, %v", b, err)
	}
	r1, _ := st.GetRequest(1)
	if r1.Status != "waiting" || r1.BookingID == 0 || r1.Contact != "jo@example.org" || r1.Phone != "" {
		t.Fatalf("request 1 = %+v", r1)
	}
	b, _ := st.GetBooking(r1.BookingID)
	if b.Status != "pending" || b.Source != SourceRequest || b.Title != "Movie night" || b.Visibility != "private" ||
		b.CreatedBy != 0 || b.StartTime != "18:00" || b.EndTime != "21:00" || b.RoomNames() != "Hall, Kitchen" {
		t.Errorf("pending booking 1 = %+v", b)
	}
	r2, _ := st.GetRequest(2)
	if b, _ := st.GetBooking(r2.BookingID); r2.Status != "waiting" || b.Status != "pending" || b.CreatedBy != 1 || b.Title != "Prayer" || b.RoomNames() != "Kitchen" {
		t.Errorf("member's waiting request = %+v, booking %+v", r2, b)
	}
	if r3, _ := st.GetRequest(3); r3.BookingID != 0 {
		t.Errorf("answered request got a booking: %+v", r3)
	}
	if rm, _ := st.GetRoom(2); rm.Kind != "" || rm.ParentID != 0 {
		t.Errorf("room = %+v", rm)
	}
	// The new statuses are allowed.
	if _, err := st.DB.Exec(`UPDATE requests SET status = 'cancelled' WHERE id = 3`); err != nil {
		t.Error(err)
	}
}
