// Package connections describes the links to other apps (the maintenance
// tracker, the event planner, later single sign-on) and works out where
// each one's values come from. The design is shared by all the apps (see
// ../event_planning/CONNECTIONS_PLAN.md):
//
//  1. Environment first, then settings, then off.
//  2. Whole connection, not per field: if any of a connection's variables
//     is set in the environment, all of it comes from the environment and
//     its settings are ignored.
//  3. Secrets are write-only on the page and sealed at rest.
package connections

import (
	"errors"

	"github.com/tylergoza/church-calendar/internal/secrets"
)

// Field is one value of a connection.
type Field struct {
	Key      string // settings key, e.g. "tracker_url"
	Env      string // environment variable, e.g. "TRACKER_URL"
	Label    string
	Hint     string
	Secret   bool   // write-only on the page, sealed in settings
	URL      bool   // an http(s) address, normalized on save
	Required bool   // the connection is off while this is blank
	Prefix   string // what a valid value starts with, e.g. "mt_"
}

// Def is a connection: its fields and how to describe it.
type Def struct {
	ID     string // "tracker", "events"; used in URLs and the clear command
	Name   string
	Help   string
	Fields []Field
}

// SecretKeys is every secret settings key, for sealing old plain values.
func SecretKeys(defs []Def) []string {
	var out []string
	for _, d := range defs {
		for _, f := range d.Fields {
			if f.Secret {
				out = append(out, f.Key)
			}
		}
	}
	return out
}

// Find is the definition with this ID.
func Find(defs []Def, id string) (Def, bool) {
	for _, d := range defs {
		if d.ID == id {
			return d, true
		}
	}
	return Def{}, false
}

// Source is where a connection's values come from.
type Source int

const (
	Off      Source = iota // nothing set: the feature is off
	Settings               // saved by an admin on /admin/connections
	Env                    // set on the server; settings ignored
)

func (s Source) String() string {
	switch s {
	case Env:
		return "Set on the server"
	case Settings:
		return "Set here"
	}
	return "Not connected"
}

// Getter reads settings; *store.Store is one.
type Getter interface {
	Setting(key, fallback string) string
	SecretSetting(key string) (string, error)
}

// Resolved is a connection's values and where they came from.
type Resolved struct {
	Def    Def
	Source Source
	// Values in use, by settings key. Empty when Off.
	Values map[string]string
	// Saved is what's in settings, by key, whatever the source.
	Saved map[string]string
	// FromServer is what's in the environment, by settings key.
	FromServer map[string]string
	// Unreadable lists saved secrets that couldn't be opened (the
	// database came from a server with another secrets.key).
	Unreadable []string
	// FromEnv is true when any of its variables is set, even if a
	// required one is missing (then Source is Off: the server said
	// something, and the settings must not quietly fill the gap).
	FromEnv bool
}

// Resolve works out a connection. env is os.Getenv in the app.
func Resolve(d Def, env func(string) string, st Getter) Resolved {
	r := Resolved{Def: d, Values: map[string]string{}, Saved: map[string]string{}, FromServer: map[string]string{}}
	for _, f := range d.Fields {
		if v := env(f.Env); v != "" {
			r.FromServer[f.Key] = v
			r.FromEnv = true
		}
		var v string
		if f.Secret {
			var err error
			v, err = st.SecretSetting(f.Key)
			if errors.Is(err, secrets.ErrUnreadable) {
				r.Unreadable = append(r.Unreadable, f.Key)
			}
		} else {
			v = st.Setting(f.Key, "")
		}
		if v != "" {
			r.Saved[f.Key] = v
		}
	}
	use := r.Saved
	src := Settings
	if r.FromEnv {
		use, src = r.FromServer, Env
	}
	for _, f := range d.Fields {
		if f.Required && use[f.Key] == "" {
			return r // off: Values stays empty
		}
	}
	if len(use) == 0 {
		return r
	}
	r.Source, r.Values = src, use
	return r
}

// Get is a value in use ("" when off).
func (r Resolved) Get(key string) string { return r.Values[key] }

// On reports whether the connection is usable.
func (r Resolved) On() bool { return r.Source != Off }

// SettingsMatch reports, for a connection set on the server, whether the
// settings hold the same values, so the environment ones can be removed
// without anything changing.
func (r Resolved) SettingsMatch() bool {
	if !r.FromEnv {
		return false
	}
	for _, f := range r.Def.Fields {
		if r.FromServer[f.Key] != r.Saved[f.Key] {
			return false
		}
	}
	return true
}

// SettingsDiffer reports, for a connection set on the server, whether
// settings hold something else. The page says so, so a stale server value
// doesn't silently win.
func (r Resolved) SettingsDiffer() bool {
	return r.FromEnv && len(r.Saved) > 0 && !r.SettingsMatch()
}

// Hint is the last four characters of a secret, for "set, ends …a1b2".
func Hint(secret string) string {
	if len(secret) <= 8 {
		return ""
	}
	return secret[len(secret)-4:]
}
