package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMacsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "macs.txt")

	contents := `# Known devices
laptop    AA:BB:CC:DD:EE:FF
phone     11-22-33-44-55-66
printer   00:11:22:33:44:55   # inline comment
not a mac: 12:34
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	got := loadMacsFromFile(path)
	want := []string{
		"aa:bb:cc:dd:ee:ff",
		"11:22:33:44:55:66",
		"00:11:22:33:44:55",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loadMacsFromFile() = %v, want %v", got, want)
	}
}

func TestLoadMacsFromFileMissing(t *testing.T) {
	if got := loadMacsFromFile("/nonexistent/macs.txt"); got != nil {
		t.Errorf("missing file should yield nil, got %v", got)
	}
}

func TestLoadIncludesFileMacs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "macs.txt")
	if err := os.WriteFile(path, []byte("AA:BB:CC:DD:EE:FF\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KNOWN_MACS", "00:11:22:33:44:55")
	t.Setenv("KNOWN_MACS_FILE", path)

	cfg := Load()
	want := []string{"00:11:22:33:44:55", "aa:bb:cc:dd:ee:ff"}
	if !reflect.DeepEqual(cfg.KnownMacs, want) {
		t.Errorf("cfg.KnownMacs = %v, want %v", cfg.KnownMacs, want)
	}
}
