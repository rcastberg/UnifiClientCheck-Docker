package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// The seed must be re-read on each call, otherwise /reload could not pick up
// edits made to the file since startup.
func TestKnownMacsSeedRereadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "macs.txt")
	if err := os.WriteFile(path, []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{KnownMacsFile: path}
	if got := cfg.KnownMacsSeed(); !reflect.DeepEqual(got, []string{"aa:bb:cc:dd:ee:ff"}) {
		t.Fatalf("initial seed = %v", got)
	}

	// Add one and remove the original.
	if err := os.WriteFile(path, []byte("11:22:33:44:55:66\n99:99:99:99:99:99\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cfg.KnownMacsSeed()
	want := []string{"11:22:33:44:55:66", "99:99:99:99:99:99"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seed after edit = %v, want %v", got, want)
	}
	if slices.Contains(got, "aa:bb:cc:dd:ee:ff") {
		t.Error("removed MAC still present; removals would not take effect on reload")
	}
}

// Env entries are held separately so repeated seeds do not accumulate copies.
func TestKnownMacsSeedDoesNotAccumulate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "macs.txt")
	if err := os.WriteFile(path, []byte("aa:bb:cc:dd:ee:ff\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		EnvKnownMacs:  []string{"00:11:22:33:44:55"},
		KnownMacsFile: path,
	}

	first := cfg.KnownMacsSeed()
	second := cfg.KnownMacsSeed()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("seed not stable across calls: %v then %v", first, second)
	}
	if len(second) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(second), second)
	}
	// The env slice itself must not have been appended to.
	if len(cfg.EnvKnownMacs) != 1 {
		t.Errorf("EnvKnownMacs was mutated: %v", cfg.EnvKnownMacs)
	}
}

func TestKnownMacsSeedWithoutFile(t *testing.T) {
	cfg := Config{EnvKnownMacs: []string{"00:11:22:33:44:55"}}
	if got := cfg.KnownMacsSeed(); !reflect.DeepEqual(got, []string{"00:11:22:33:44:55"}) {
		t.Errorf("seed = %v, want just the env entry", got)
	}
}

func TestReloadCommandDefaultsAndNormalises(t *testing.T) {
	t.Setenv("SLACK_RELOAD_COMMAND", "")
	if got := Load().SlackReloadCmd; got != "/reload" {
		t.Errorf("default reload command = %q, want /reload", got)
	}

	t.Setenv("SLACK_RELOAD_COMMAND", "refresh")
	if got := Load().SlackReloadCmd; got != "/refresh" {
		t.Errorf("reload command = %q, want /refresh", got)
	}
}
