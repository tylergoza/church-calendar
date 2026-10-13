package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/connections"
	"github.com/tylergoza/church-calendar/internal/sso"
	"github.com/tylergoza/church-calendar/internal/store"
)

// Single sign-on from the Connections page --------------------------------
//
// A wrong SSO setting can lock every admin out, so saving never changes
// sign-in. The values wait as sso_pending_*; Test sign-in sends the admin
// through User Management with them, and only when they come back as an
// admin of this app do the pending values become the live ones. See
// CONNECTIONS_PLAN.md, "Single sign-on needs care".

const (
	ssoPendingPrefix = "sso_pending_"
	// ssoTestCookie holds the state, PKCE verifier and a hash of the
	// admin's session token for a test sign-in. Separate from cal_sso so a
	// test never touches a normal sign-in under way.
	ssoTestCookie = "cal_sso_test"
)

// pendingKey is the settings key that holds the pending value for a live
// key: sso_url → sso_pending_url, base_url → sso_pending_base_url.
func pendingKey(live string) string {
	return ssoPendingPrefix + strings.TrimPrefix(live, "sso_")
}

// ssoDef is the single sign-on connection's definition.
func ssoDef() connections.Def {
	d, _ := connections.Find(connectionDefs, "sso")
	return d
}

// pendingValues are the SSO values waiting for a test sign-in, by the live
// keys, so buildSSO and the page read them like the connection's own.
// Unreadable is set when the pending secret can't be opened (it's blank).
type pendingValues struct {
	Values     map[string]string
	Unreadable bool
}

func (p pendingValues) Get(key string) string { return p.Values[key] }

// pendingSSO is the pending values; ok is false when nothing is pending.
func (s *Server) pendingSSO() (pendingValues, bool) {
	p := pendingValues{Values: map[string]string{}}
	for _, f := range ssoDef().Fields {
		k := pendingKey(f.Key)
		v := s.store.Setting(k, "")
		if f.Secret {
			var err error
			if v, err = s.store.SecretSetting(k); err != nil {
				p.Unreadable = true
				v = ""
			}
		}
		p.Values[f.Key] = v
	}
	return p, p.Get("sso_url") != ""
}

// pendingOrigin is " <origin>" of the pending User Management address, for
// the CSP's form-action on the Connections page (the Test sign-in form's
// redirect goes there), or "".
func (s *Server) pendingOrigin() string {
	u, err := url.Parse(s.store.Setting("sso_pending_url", ""))
	if err != nil || u.Host == "" {
		return ""
	}
	return " " + u.Scheme + "://" + u.Host
}

func (s *Server) clearPendingSSO() error { return clearPendingSSO(s.store) }

func clearPendingSSO(st *store.Store) error {
	for _, f := range ssoDef().Fields {
		if err := st.SetSetting(pendingKey(f.Key), ""); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) ssoBack(w http.ResponseWriter, r *http.Request, kind, msg string) {
	s.setFlash(w, r, kind, msg)
	http.Redirect(w, r, "/admin/connections#sso", http.StatusSeeOther)
}

// ssoRefused words a failed server-to-server check for the page.
func ssoRefused(err error, address string) string {
	var ae *sso.APIError
	switch {
	case errors.As(err, &ae) && ae.Code == "invalid_client":
		return "User Management refused the client ID or secret. Check them against this app's entry on User Management's Apps page."
	case errors.As(err, &ae):
		return fmt.Sprintf("%s doesn't look like User Management (it answered HTTP %d).", address, ae.Status)
	case errors.Is(err, sso.ErrUnreachable):
		return fmt.Sprintf("Can't reach %s: %v", address, err)
	}
	return "Couldn't check with User Management: " + err.Error()
}

// checkSSOClient runs the server check on a client.
func checkSSOClient(ctx context.Context, c *sso.Client) (ok bool, msg string) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if err := c.CheckClient(ctx); err != nil {
		return false, ssoRefused(err, c.InternalURL)
	}
	return true, "User Management accepted the client ID and secret."
}

// handleSSOCheck is the section's Test button: it checks the live
// settings (or the server's) without any sign-in.
func (s *Server) handleSSOCheck(w http.ResponseWriter, r *http.Request) {
	c := s.SSO()
	if v := s.connection("sso"); !v.FromEnv && len(v.Unreadable) > 0 {
		s.ssoBack(w, r, "error", "Can't read the saved secret: enter it again.")
		return
	}
	if b := s.SSOBroken(); b != "" {
		s.ssoBack(w, r, "error", "Single sign-on settings are broken: "+b)
		return
	}
	if !c.Enabled() {
		s.ssoBack(w, r, "success", "Single sign-on isn't on, so people use local passwords.")
		return
	}
	ok, msg := checkSSOClient(r.Context(), c)
	kind := "success"
	if !ok {
		kind = "error"
	}
	s.ssoBack(w, r, kind, msg)
}

// handleSSOSave stores the form as pending. Live sign-in is untouched.
func (s *Server) handleSSOSave(w http.ResponseWriter, r *http.Request) {
	if s.connection("sso").FromEnv {
		s.ssoBack(w, r, "error", "Single sign-on is set on the server, so it can't be changed here.")
		return
	}
	values := map[string]string{}
	var errs []string
	for _, f := range ssoDef().Fields {
		v := formStr(r, f.Key)
		if f.URL {
			n, err := normalizeURL(v)
			if err != nil {
				errs = append(errs, f.Label+" should be an address like https://example.org.")
			}
			v = n
		}
		values[f.Key] = v
	}
	if values["sso_url"] == "" {
		errs = append(errs, "User Management address is needed. To turn single sign-on off, use Turn off.")
	}
	if values["sso_client_id"] == "" {
		errs = append(errs, "Client ID is needed (from this app's entry in User Management).")
	}
	// A blank secret keeps the pending one, else the live one.
	secret := values["sso_client_secret"]
	if secret == "" {
		if pv, ok := s.pendingSSO(); ok {
			secret = pv.Get("sso_client_secret")
		}
	}
	if secret == "" {
		secret = s.connection("sso").Saved["sso_client_secret"]
	}
	if secret == "" {
		errs = append(errs, "Client secret is needed (from this app's entry in User Management).")
	}
	values["sso_client_secret"] = secret
	if len(errs) == 0 {
		// The same checks the live settings get, so a pending set that
		// could never turn on is refused now.
		if _, _, err := buildSSO(values); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if errs != nil {
		s.renderConnections(w, r, http.StatusUnprocessableEntity, "sso", errs, r.PostForm)
		return
	}
	for _, f := range ssoDef().Fields {
		var err error
		if f.Secret {
			err = s.store.SetSecretSetting(pendingKey(f.Key), values[f.Key])
		} else {
			err = s.store.SetSetting(pendingKey(f.Key), values[f.Key])
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	by := currentUser(r).Username
	s.log.Info("single sign-on pending values saved", slog.String("by", by))
	msg := "Saved as pending. Nothing has changed yet: press Test sign-in and sign in as an admin of this app to make it live."
	c := sso.New(values["sso_url"], values["sso_internal_url"], values["sso_client_id"], values["sso_client_secret"])
	kind := "success"
	if ok, check := checkSSOClient(r.Context(), c); !ok {
		kind, msg = "error", msg+" Note: "+check
	}
	s.ssoBack(w, r, kind, msg)
}

// handleSSODiscard drops the pending values.
func (s *Server) handleSSODiscard(w http.ResponseWriter, r *http.Request) {
	if err := s.clearPendingSSO(); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("single sign-on pending values discarded", slog.String("by", currentUser(r).Username))
	s.ssoBack(w, r, "success", "Pending values discarded. Sign-in is as it was.")
}

// handleSSOOff turns live single sign-on off, back to local passwords.
func (s *Server) handleSSOOff(w http.ResponseWriter, r *http.Request) {
	var live []string
	for _, f := range ssoDef().Fields {
		if err := s.store.SetSetting(f.Key, ""); err != nil {
			s.serverError(w, r, err)
			return
		}
		live = append(live, f.Key)
	}
	s.noteConnectionChange(r, "sso", "turn off", live)
	msg := "Single sign-on is off. People now sign in with local passwords; use church-calendar reset-password on the server for anyone who has none."
	if s.SSO().Enabled() {
		msg = "Saved single sign-on settings removed. It's still set on the server, so it stays on."
	}
	s.ssoBack(w, r, "success", msg)
}

// Test sign-in ---------------------------------------------------------------

// sessionHash binds a test sign-in to the admin's session without putting
// the session token itself in another cookie.
func sessionHash(r *http.Request) string {
	sess, _ := r.Context().Value(ctxSession).(*store.Session)
	if sess == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(sess.Token))
	return hex.EncodeToString(sum[:])
}

// requestBaseURL is this app's address as the request shows it, ignoring
// the configured one.
func (s *Server) requestBaseURL(r *http.Request) string {
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

// pendingClient builds the one-off client for the pending values and the
// address its sign-in comes back to.
func (s *Server) pendingClient(r *http.Request) (c *sso.Client, redirectURI string, err error) {
	pv, ok := s.pendingSSO()
	if !ok {
		return nil, "", errors.New("There are no pending values to test.")
	}
	if pv.Unreadable {
		return nil, "", errors.New("Can't read the pending client secret: save it again.")
	}
	c, base, err := buildSSO(pv.Values)
	if err != nil {
		return nil, "", err
	}
	if base == "" {
		base = s.requestBaseURL(r)
	}
	return c, base + "/auth/callback", nil
}

// handleSSOTestSignIn starts the sign-in with the pending values. The
// state is bound to this admin's session.
func (s *Server) handleSSOTestSignIn(w http.ResponseWriter, r *http.Request) {
	if s.connection("sso").FromEnv {
		s.ssoBack(w, r, "error", "Single sign-on is set on the server, so it can't be changed here.")
		return
	}
	c, redirectURI, err := s.pendingClient(r)
	if err != nil {
		s.ssoBack(w, r, "error", err.Error())
		return
	}
	// Check the client ID and secret server to server first, so a typo is
	// reported here rather than as an error page at User Management.
	if ok, msg := checkSSOClient(r.Context(), c); !ok {
		s.ssoBack(w, r, "error", msg+" Pending values were kept; change them and try again.")
		return
	}
	auth, err := c.Authorize(redirectURI)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: ssoTestCookie, Value: auth.State + "." + auth.Verifier + "." + sessionHash(r),
		Path: "/auth/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r),
		MaxAge: int(ssoCookieTTL.Seconds()),
	})
	s.log.Info("single sign-on test sign-in started", slog.String("by", currentUser(r).Username))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, auth.URL, http.StatusSeeOther)
}

// readSSOTestCookie splits the test cookie into state, verifier and the
// session hash.
func readSSOTestCookie(r *http.Request) (state, verifier, bound string, ok bool) {
	c, err := r.Cookie(ssoTestCookie)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// isSSOTestCallback reports whether this callback belongs to a test
// sign-in: the state it carries is the one in the test cookie.
func isSSOTestCallback(r *http.Request) bool {
	state, _, _, ok := readSSOTestCookie(r)
	q := r.URL.Query().Get("state")
	return ok && q != "" && subtle.ConstantTimeCompare([]byte(q), []byte(state)) == 1
}

// handleSSOTestCallback finishes a test sign-in. The current session stays
// as it is whatever happens here; the only change is the pending values
// becoming live, and only for a person who is an admin of this app.
func (s *Server) handleSSOTestCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{Name: ssoTestCookie, Path: "/auth/", MaxAge: -1})
	_, verifier, bound, _ := readSSOTestCookie(r)

	me := currentUser(r)
	if me == nil || !me.IsAdmin() || subtle.ConstantTimeCompare([]byte(bound), []byte(sessionHash(r))) != 1 {
		s.log.Warn("sso test callback: not the admin who started it", "ip", s.clientIP(r))
		s.render(w, r, http.StatusForbidden, "signin_problem", map[string]any{"Title": "Couldn't finish the test",
			"Message": "That test sign-in was started by someone else, or you've signed out since. Sign in as an admin and start the test again."})
		return
	}
	fail := func(msg string) {
		s.log.Warn("single sign-on test failed", slog.String("by", me.Username), slog.String("why", msg))
		s.ssoBack(w, r, "error", "Test sign-in failed, so nothing changed: "+msg+" Pending values were kept.")
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			fail("User Management says you don't have access to this app.")
		} else {
			fail("User Management couldn't sign you in (" + e + ").")
		}
		return
	}
	if q.Get("code") == "" {
		fail("the sign-in link was incomplete.")
		return
	}
	c, redirectURI, err := s.pendingClient(r)
	if err != nil {
		fail(strings.TrimSuffix(err.Error(), "."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	_, u, err := c.Exchange(ctx, q.Get("code"), redirectURI, verifier)
	if err != nil {
		var ae *sso.APIError
		switch {
		case errors.As(err, &ae) && ae.Code == "invalid_client":
			fail("User Management refused the client secret.")
		case errors.As(err, &ae):
			fail(ae.Message())
		case errors.Is(err, sso.ErrUnreachable):
			fail("User Management can't be reached right now.")
		default:
			fail("something went wrong exchanging the sign-in.")
		}
		return
	}
	if u.Sub == "" {
		fail("User Management didn't say who signed in.")
		return
	}
	role := store.RoleFromSSO(u.Role)
	if role != store.RoleAdmin {
		who := u.Name
		if who == "" {
			who = u.Username
		}
		fail(fmt.Sprintf("you signed in as %s, who isn't an admin of this app in User Management.", who))
		return
	}

	// Link the account the way the first rollout does, then go live.
	if _, err := s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, role); err != nil {
		s.serverError(w, r, err)
		return
	}
	pv, _ := s.pendingSSO()
	var live []string
	for _, f := range ssoDef().Fields {
		var err error
		if f.Secret {
			err = s.store.SetSecretSetting(f.Key, pv.Get(f.Key))
		} else {
			err = s.store.SetSetting(f.Key, pv.Get(f.Key))
		}
		if err != nil {
			s.reloadSSO()
			s.serverError(w, r, err)
			return
		}
		live = append(live, f.Key)
	}
	if err := s.clearPendingSSO(); err != nil {
		s.log.Error("single sign-on: clear pending after going live", "err", err)
	}
	s.noteConnectionChange(r, "sso", "go live", live)
	s.log.Info("single sign-on is live", slog.String("by", me.Username), slog.String("signed_in_as", u.Username))
	s.ssoBack(w, r, "success", "Single sign-on is live. You signed in as "+u.Username+"; you stay signed in here, and everyone else now signs in through User Management.")
}
