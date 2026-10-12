package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/connections"
	"github.com/tylergoza/church-calendar/internal/store"
)

// Connections to other apps -------------------------------------------------

// connectionDefs are the calendar's links to other apps. Each is optional:
// when it's off, the app behaves as if the other app doesn't exist. Single
// sign-on joins this list with its Test sign-in step (PLAN.md step 7).
var connectionDefs = []connections.Def{
	{
		ID: "tracker", Name: "Maintenance tracker",
		Help: `Import rooms from the tracker's places. In the tracker, an admin opens API, makes a token for "Calendar", and pastes it here.`,
		Fields: []connections.Field{
			{Key: "tracker_url", Env: "TRACKER_URL", Label: "Address", Hint: "https://maintenance.example.org", URL: true, Required: true},
			{Key: "tracker_token", Env: "TRACKER_TOKEN", Label: "API token", Hint: "mt_…", Secret: true, Required: true, Prefix: "mt_"},
		},
	},
	{
		ID: "events", Name: "Event planner",
		Help: "Turns on Make this an event for bookings and requests. Only the address is needed here: the event planner sends its events to the calendar with a token made under API tokens.",
		Fields: []connections.Field{
			{Key: "events_url", Env: "EVENTS_URL", Label: "Address", Hint: "https://events.example.org", URL: true, Required: true},
		},
	},
}

// connectionChecks test a connection live and answer in plain words. A
// connection without one has no Test button yet.
var connectionChecks = map[string]func(context.Context, connections.Resolved) error{
	"events": func(ctx context.Context, r connections.Resolved) error {
		return checkHealthz(ctx, r.Get("events_url"))
	},
}

// connection resolves one connection now: environment, then settings.
func (s *Server) connection(id string) connections.Resolved {
	d, ok := connections.Find(connectionDefs, id)
	if !ok {
		panic("unknown connection " + id)
	}
	return connections.Resolve(d, s.cfg.Env, s.store)
}

// EventsURL is the event planner's address, or "" when it isn't connected.
func (s *Server) EventsURL() string { return s.connection("events").Get("events_url") }

// changedKey holds "who|when" for a connection's last change.
func changedKey(id string) string { return "connection_" + id + "_changed" }

type connField struct {
	connections.Field
	Value      string // non-secret: what's shown in the box
	Set        bool   // secret: there is one
	Ends       string // secret: its last four characters
	Unreadable bool   // secret: saved with another secrets.key
}

type connView struct {
	connections.Resolved
	Fields    []connField
	ChangedBy string
	ChangedAt string
	CanTest   bool
}

func (s *Server) connectionView(d connections.Def, form url.Values) connView {
	r := connections.Resolve(d, s.cfg.Env, s.store)
	v := connView{Resolved: r, CanTest: connectionChecks[d.ID] != nil}
	if who, when, ok := strings.Cut(s.store.Setting(changedKey(d.ID), ""), "|"); ok {
		v.ChangedBy, v.ChangedAt = who, when
	}
	for _, f := range d.Fields {
		cf := connField{Field: f}
		vals := r.Saved
		if r.FromEnv {
			vals = r.FromServer
		}
		if f.Secret {
			cf.Set = vals[f.Key] != ""
			cf.Ends = connections.Hint(vals[f.Key])
			cf.Unreadable = !r.FromEnv && slices.Contains(r.Unreadable, f.Key)
		} else if form != nil {
			cf.Value = form.Get(f.Key)
		} else {
			cf.Value = vals[f.Key]
		}
		v.Fields = append(v.Fields, cf)
	}
	return v
}

// renderConnections shows the page; errs and form belong to the section
// with ID errFor, sent back with what was typed.
func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, status int, errFor string, errs []string, form url.Values) {
	var views []connView
	for _, d := range connectionDefs {
		var f url.Values
		if d.ID == errFor {
			f = form
		}
		views = append(views, s.connectionView(d, f))
	}
	s.render(w, r, status, "connections", map[string]any{
		"Title": "Connections", "Connections": views, "ErrFor": errFor, "Errors": errs,
	})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	s.renderConnections(w, r, http.StatusOK, "", nil, nil)
}

// connectionFor is the connection named in the path, or a 404.
func (s *Server) connectionFor(w http.ResponseWriter, r *http.Request) (connections.Def, bool) {
	d, ok := connections.Find(connectionDefs, r.PathValue("id"))
	if !ok {
		s.notFound(w, r)
	}
	return d, ok
}

func (s *Server) handleConnectionSave(w http.ResponseWriter, r *http.Request) {
	d, ok := s.connectionFor(w, r)
	if !ok {
		return
	}
	if connections.Resolve(d, s.cfg.Env, s.store).FromEnv {
		s.setFlash(w, r, "error", d.Name+" is set on the server, so it can't be changed here.")
		http.Redirect(w, r, "/admin/connections#"+d.ID, http.StatusSeeOther)
		return
	}
	set, clear := map[string]string{}, map[string]bool{}
	var errs []string
	for _, f := range d.Fields {
		v := formStr(r, f.Key)
		if f.Secret {
			switch {
			case r.PostFormValue("remove_"+f.Key) == "1":
				clear[f.Key] = true
			case v != "":
				if f.Prefix != "" && !strings.HasPrefix(v, f.Prefix) {
					errs = append(errs, fmt.Sprintf("The %s should start with %s.", strings.ToLower(f.Label), f.Prefix))
				}
				set[f.Key] = v
			}
			continue
		}
		if f.URL {
			n, err := normalizeURL(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("The %s's address should look like %s.", d.Name, f.Hint))
			}
			v = n
		}
		set[f.Key] = v
	}
	if errs != nil {
		s.renderConnections(w, r, http.StatusUnprocessableEntity, d.ID, errs, r.PostForm)
		return
	}
	var changed []string
	for _, f := range d.Fields {
		var err error
		switch {
		case clear[f.Key]:
			err = s.store.SetSetting(f.Key, "")
		case f.Secret && set[f.Key] != "":
			err = s.store.SetSecretSetting(f.Key, set[f.Key])
		case !f.Secret:
			if s.store.Setting(f.Key, "") == set[f.Key] {
				continue
			}
			err = s.store.SetSetting(f.Key, set[f.Key])
		default:
			continue
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		changed = append(changed, f.Key)
	}
	s.noteConnectionChange(r, d.ID, "save", changed)
	s.redirectAfterConnectionChange(w, r, d, "Saved.")
}

// handleConnectionKeep copies a connection's server values into the
// settings, so they can then be removed from the server without anything
// disconnecting.
func (s *Server) handleConnectionKeep(w http.ResponseWriter, r *http.Request) {
	d, ok := s.connectionFor(w, r)
	if !ok {
		return
	}
	res := connections.Resolve(d, s.cfg.Env, s.store)
	if !res.FromEnv {
		s.redirect(w, r, "/admin/connections#"+d.ID, "")
		return
	}
	var keys []string
	for _, f := range d.Fields {
		v := res.FromServer[f.Key]
		var err error
		if f.Secret {
			err = s.store.SetSecretSetting(f.Key, v)
		} else {
			err = s.store.SetSetting(f.Key, v)
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		keys = append(keys, f.Key)
	}
	s.noteConnectionChange(r, d.ID, "keep in settings", keys)
	s.redirect(w, r, "/admin/connections#"+d.ID,
		"The server's values are saved here too. You can now remove them from the server; nothing will disconnect.")
}

// handleConnectionClear removes a connection's saved settings.
func (s *Server) handleConnectionClear(w http.ResponseWriter, r *http.Request) {
	d, ok := s.connectionFor(w, r)
	if !ok {
		return
	}
	if err := ClearConnection(s.store, d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.noteConnectionChange(r, d.ID, "clear", nil)
	msg := d.Name + ": saved settings removed."
	if connections.Resolve(d, s.cfg.Env, s.store).FromEnv {
		msg += " It's still set on the server, so it stays connected."
	}
	s.redirect(w, r, "/admin/connections#"+d.ID, msg)
}

func (s *Server) handleConnectionTest(w http.ResponseWriter, r *http.Request) {
	d, ok := s.connectionFor(w, r)
	if !ok {
		return
	}
	s.redirectAfterConnectionChange(w, r, d, "")
}

// redirectAfterConnectionChange tests the connection (when it can be) and
// goes back to the page with the answer. A failed test doesn't undo a
// save: the other app may not be running yet.
func (s *Server) redirectAfterConnectionChange(w http.ResponseWriter, r *http.Request, d connections.Def, prefix string) {
	to := "/admin/connections#" + d.ID
	res := connections.Resolve(d, s.cfg.Env, s.store)
	check := connectionChecks[d.ID]
	say := func(kind, msg string) {
		s.setFlash(w, r, kind, strings.TrimSpace(prefix+" "+msg))
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
	switch {
	case !res.On():
		say("info", d.Name+" is not connected.")
	case check == nil:
		say("success", d.Name+" is set.")
	default:
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := check(ctx, res); err != nil {
			say("error", d.Name+": "+err.Error())
			return
		}
		say("success", "Connected to the "+strings.ToLower(d.Name)+".")
	}
}

// noteConnectionChange records who changed a connection and logs it. Only
// field names are logged, never values.
func (s *Server) noteConnectionChange(r *http.Request, id, action string, fields []string) {
	u := currentUser(r)
	s.store.SetSetting(changedKey(id), u.Username+"|"+time.Now().UTC().Format("2006-01-02 15:04:05"))
	s.log.Info("connection changed", "user", u.Username, "connection", id, "action", action, "fields", strings.Join(fields, ","))
}

// ClearConnection removes a connection's saved settings. It's also the
// break-glass `connection clear` command, for when a bad saved setting is
// the problem.
func ClearConnection(st *store.Store, id string) error {
	d, ok := connections.Find(connectionDefs, id)
	if !ok {
		return fmt.Errorf("unknown connection %q (use %s)", id, strings.Join(ConnectionIDs(), ", "))
	}
	for _, f := range d.Fields {
		if err := st.SetSetting(f.Key, ""); err != nil {
			return err
		}
	}
	return nil
}

// ConnectionIDs lists the connections, for the clear command's usage.
func ConnectionIDs() []string {
	var ids []string
	for _, d := range connectionDefs {
		ids = append(ids, d.ID)
	}
	return ids
}

// checkHealthz asks one of the sibling apps whether it's up.
func checkHealthz(ctx context.Context, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s didn't answer in time", base)
		}
		return fmt.Errorf("can't reach %s", base)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		return fmt.Errorf("%s answered, but not like one of the church apps (status %d)", base, resp.StatusCode)
	}
	return nil
}

// normalizeURL checks an address and trims it to "scheme://host[:port][/path]"
// with no trailing slash. Blank is allowed.
func normalizeURL(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("bad address")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}
