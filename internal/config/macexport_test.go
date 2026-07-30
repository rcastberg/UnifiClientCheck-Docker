package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// An export must be readable by the importer, so a dumped list can be fed back
// in via KNOWN_MACS_FILE.
func TestWriteMacsFileRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_macs.txt")
	macs := []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}

	if err := WriteMacsFile(path, macs); err != nil {
		t.Fatalf("WriteMacsFile: %v", err)
	}

	if got := loadMacsFromFile(path); !reflect.DeepEqual(got, macs) {
		t.Errorf("round trip produced %v, want %v", got, macs)
	}
}

// The header must not contain anything the importer would mistake for a device.
func TestWriteMacsFileHeaderIsNotImported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_macs.txt")

	if err := WriteMacsFile(path, nil); err != nil {
		t.Fatalf("WriteMacsFile: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 {
		t.Fatal("expected a header even with no MACs")
	}
	if got := loadMacsFromFile(path); len(got) != 0 {
		t.Errorf("header parsed as MACs: %v", got)
	}
}

func TestWriteMacsFileOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_macs.txt")

	if err := WriteMacsFile(path, []string{"aa:bb:cc:dd:ee:ff", "11:22:33:44:55:66"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteMacsFile(path, []string{"99:99:99:99:99:99"}); err != nil {
		t.Fatal(err)
	}

	got := loadMacsFromFile(path)
	want := []string{"99:99:99:99:99:99"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("after overwrite got %v, want %v", got, want)
	}
}

// The atomic write creates a temporary file alongside the target; it must not
// be left behind, or the next import could pick up a stale copy.
func TestWriteMacsFileLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_macs.txt")

	if err := WriteMacsFile(path, []string{"aa:bb:cc:dd:ee:ff"}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "known_macs.txt" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only known_macs.txt", names)
	}
}

func TestWriteMacsFileCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "known_macs.txt")

	if err := WriteMacsFile(path, []string{"aa:bb:cc:dd:ee:ff"}); err != nil {
		t.Fatalf("WriteMacsFile: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestExportCommandDefaultsAndNormalises(t *testing.T) {
	t.Setenv("SLACK_EXPORT_COMMAND", "")
	if got := Load().SlackExportCmd; got != "/writemacs" {
		t.Errorf("default export command = %q, want /writemacs", got)
	}

	// A leading slash is optional in configuration.
	t.Setenv("SLACK_EXPORT_COMMAND", "dumpmacs")
	if got := Load().SlackExportCmd; got != "/dumpmacs" {
		t.Errorf("export command = %q, want /dumpmacs", got)
	}

	t.Setenv("SLACK_EXPORT_COMMAND", "/dumpmacs")
	if got := Load().SlackExportCmd; got != "/dumpmacs" {
		t.Errorf("export command = %q, want /dumpmacs", got)
	}
}
