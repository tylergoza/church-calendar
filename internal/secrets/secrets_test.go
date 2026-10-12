package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), KeyFile)
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode: %v %v", fi.Mode(), err)
	}
	sealed, err := b.Seal("mt_secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, Prefix) || strings.Contains(sealed, "mt_secret") {
		t.Fatalf("sealed = %q", sealed)
	}
	// The same key file opens it again.
	b2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b2.Open(sealed); err != nil || got != "mt_secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if got, _ := b.Seal(""); got != "" {
		t.Errorf("Seal(\"\") = %q, want empty", got)
	}
}

func TestWrongKey(t *testing.T) {
	a, _ := LoadOrCreate(filepath.Join(t.TempDir(), KeyFile))
	b, _ := LoadOrCreate(filepath.Join(t.TempDir(), KeyFile))
	sealed, _ := a.Seal("cal_token")
	if _, err := b.Open(sealed); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
	if _, err := b.Open(Prefix + "not base64!"); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("garbage: err = %v, want ErrUnreadable", err)
	}
}

func TestPlainPassesThrough(t *testing.T) {
	b, _ := LoadOrCreate(filepath.Join(t.TempDir(), KeyFile))
	if got, err := b.Open("mt_plain"); err != nil || got != "mt_plain" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestBadKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), KeyFile)
	os.WriteFile(path, []byte("short"), 0o600)
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("expected an error for a short key")
	}
}
