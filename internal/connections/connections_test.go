package connections

import (
	"testing"

	"github.com/tylergoza/church-calendar/internal/secrets"
)

var tracker = Def{ID: "tracker", Name: "Maintenance tracker", Fields: []Field{
	{Key: "tracker_url", Env: "TRACKER_URL", Required: true, URL: true},
	{Key: "tracker_token", Env: "TRACKER_TOKEN", Required: true, Secret: true},
}}

type fakeSettings struct {
	vals       map[string]string
	unreadable map[string]bool
}

func (f fakeSettings) Setting(k, fb string) string {
	if v, ok := f.vals[k]; ok {
		return v
	}
	return fb
}

func (f fakeSettings) SecretSetting(k string) (string, error) {
	if f.unreadable[k] {
		return "", secrets.ErrUnreadable
	}
	return f.vals[k], nil
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestOrder(t *testing.T) {
	saved := fakeSettings{vals: map[string]string{"tracker_url": "https://saved", "tracker_token": "mt_saved"}}

	r := Resolve(tracker, envOf(nil), fakeSettings{})
	if r.Source != Off || r.On() {
		t.Errorf("nothing set: source %v", r.Source)
	}
	r = Resolve(tracker, envOf(nil), saved)
	if r.Source != Settings || r.Get("tracker_token") != "mt_saved" {
		t.Errorf("settings: %v %v", r.Source, r.Values)
	}
	r = Resolve(tracker, envOf(map[string]string{"TRACKER_URL": "https://env", "TRACKER_TOKEN": "mt_env"}), saved)
	if r.Source != Env || r.Get("tracker_url") != "https://env" || r.Get("tracker_token") != "mt_env" {
		t.Errorf("env: %v %v", r.Source, r.Values)
	}
	if !r.SettingsDiffer() || r.SettingsMatch() {
		t.Error("env and settings differ")
	}
}

// One env var set means the whole connection comes from the environment:
// the saved token must not fill in the missing one.
func TestWholeConnection(t *testing.T) {
	saved := fakeSettings{vals: map[string]string{"tracker_url": "https://saved", "tracker_token": "mt_saved"}}
	r := Resolve(tracker, envOf(map[string]string{"TRACKER_URL": "https://env"}), saved)
	if r.On() || !r.FromEnv || r.Get("tracker_token") != "" {
		t.Errorf("partial env: on=%v fromEnv=%v values=%v", r.On(), r.FromEnv, r.Values)
	}
}

func TestSettingsMatch(t *testing.T) {
	both := map[string]string{"tracker_url": "https://x", "tracker_token": "mt_abc"}
	r := Resolve(tracker, envOf(map[string]string{"TRACKER_URL": "https://x", "TRACKER_TOKEN": "mt_abc"}), fakeSettings{vals: both})
	if !r.SettingsMatch() || r.SettingsDiffer() {
		t.Error("settings match the environment")
	}
	r = Resolve(tracker, envOf(map[string]string{"TRACKER_URL": "https://x", "TRACKER_TOKEN": "mt_abc"}), fakeSettings{})
	if r.SettingsMatch() || r.SettingsDiffer() {
		t.Error("no settings: neither match nor differ")
	}
}

func TestUnreadable(t *testing.T) {
	st := fakeSettings{vals: map[string]string{"tracker_url": "https://x"}, unreadable: map[string]bool{"tracker_token": true}}
	r := Resolve(tracker, envOf(nil), st)
	if r.On() || len(r.Unreadable) != 1 || r.Unreadable[0] != "tracker_token" {
		t.Errorf("unreadable: on=%v %v", r.On(), r.Unreadable)
	}
}

func TestHint(t *testing.T) {
	if Hint("mt_abcdefgh1234") != "1234" || Hint("short") != "" {
		t.Error("hint")
	}
}
