package database

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zsamuels28/unificlientalerts/internal/unifi"
)

func newTestDB(t *testing.T) (*Database, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "knownMacs.db")
	db, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func loaded(t *testing.T, db *Database) []string {
	t.Helper()
	list, err := db.LoadKnownMacs(nil)
	if err != nil {
		t.Fatalf("LoadKnownMacs: %v", err)
	}
	slices.Sort(list)
	return list
}

func TestAllowMacPermanent(t *testing.T) {
	db, _ := newTestDB(t)

	if err := db.AllowMac("aa:bb:cc:dd:ee:ff", nil); err != nil {
		t.Fatalf("AllowMac: %v", err)
	}
	if got := loaded(t, db); !slices.Contains(got, "aa:bb:cc:dd:ee:ff") {
		t.Errorf("permanent allow missing from known MACs: %v", got)
	}
}

func TestAllowMacTemporary(t *testing.T) {
	db, _ := newTestDB(t)

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	if err := db.AllowMac("11:11:11:11:11:11", &future); err != nil {
		t.Fatalf("AllowMac future: %v", err)
	}
	if err := db.AllowMac("22:22:22:22:22:22", &past); err != nil {
		t.Fatalf("AllowMac past: %v", err)
	}

	got := loaded(t, db)
	if !slices.Contains(got, "11:11:11:11:11:11") {
		t.Errorf("live temporary allow should be known: %v", got)
	}
	if slices.Contains(got, "22:22:22:22:22:22") {
		t.Errorf("lapsed temporary allow should not be known: %v", got)
	}
}

func TestAllowMacOverwritesExisting(t *testing.T) {
	db, _ := newTestDB(t)

	if err := db.UpdateKnownMacs("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("UpdateKnownMacs: %v", err)
	}
	// Re-allowing temporarily must replace the permanent entry, not fail on the
	// UNIQUE constraint.
	future := time.Now().Add(time.Hour)
	if err := db.AllowMac("aa:bb:cc:dd:ee:ff", &future); err != nil {
		t.Fatalf("AllowMac over existing row: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	if err := db.AllowMac("aa:bb:cc:dd:ee:ff", &past); err != nil {
		t.Fatalf("AllowMac downgrade: %v", err)
	}
	if got := loaded(t, db); slices.Contains(got, "aa:bb:cc:dd:ee:ff") {
		t.Errorf("expired overwrite should not be known: %v", got)
	}
}

func TestPurgeExpired(t *testing.T) {
	db, _ := newTestDB(t)

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	if err := db.AllowMac("11:11:11:11:11:11", &future); err != nil {
		t.Fatal(err)
	}
	if err := db.AllowMac("22:22:22:22:22:22", &past); err != nil {
		t.Fatal(err)
	}
	if err := db.AllowMac("33:33:33:33:33:33", nil); err != nil {
		t.Fatal(err)
	}

	expired, err := db.PurgeExpired()
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if len(expired) != 1 || expired[0] != "22:22:22:22:22:22" {
		t.Errorf("PurgeExpired returned %v, want [22:22:22:22:22:22]", expired)
	}

	got := loaded(t, db)
	want := []string{"11:11:11:11:11:11", "33:33:33:33:33:33"}
	if !slices.Equal(got, want) {
		t.Errorf("after purge got %v, want %v", got, want)
	}

	// A second sweep with nothing to do must be a no-op.
	again, err := db.PurgeExpired()
	if err != nil {
		t.Fatalf("second PurgeExpired: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second purge returned %v, want none", again)
	}
}

// A temporary allow means "stay quiet until this time" regardless of whether the
// device stays connected, so RemoveOldMacs must not reap it early.
func TestRemoveOldMacsKeepsLiveTemporaryAllow(t *testing.T) {
	db, _ := newTestDB(t)

	future := time.Now().Add(time.Hour)
	if err := db.AllowMac("11:11:11:11:11:11", &future); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateKnownMacs("99:99:99:99:99:99"); err != nil {
		t.Fatal(err)
	}

	// Mark both as long absent, with no clients online.
	if _, err := db.db.Exec("UPDATE known_macs SET last_seen = ?", time.Now().Add(-48*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	if err := db.RemoveOldMacs(nil, 60); err != nil {
		t.Fatalf("RemoveOldMacs: %v", err)
	}

	got := loaded(t, db)
	if !slices.Contains(got, "11:11:11:11:11:11") {
		t.Errorf("live temporary allow was reaped: %v", got)
	}
	if slices.Contains(got, "99:99:99:99:99:99") {
		t.Errorf("ordinary absent device should have been forgotten: %v", got)
	}
}

func TestRemoveOldMacsClearsLastSeenForOnlineDevice(t *testing.T) {
	db, _ := newTestDB(t)

	if err := db.UpdateKnownMacs("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec("UPDATE known_macs SET last_seen = ?", time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	clients := []unifi.NetworkClient{{Mac: "aa:bb:cc:dd:ee:ff"}}
	if err := db.RemoveOldMacs(clients, 60); err != nil {
		t.Fatalf("RemoveOldMacs: %v", err)
	}

	var lastSeen sql.NullInt64
	if err := db.db.QueryRow(
		"SELECT last_seen FROM known_macs WHERE mac_address = ?", "aa:bb:cc:dd:ee:ff",
	).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen.Valid {
		t.Errorf("last_seen should be cleared for an online device, got %v", lastSeen.Int64)
	}
}

// Databases created before expires_at existed must be upgraded in place, with
// their rows intact.
func TestMigrationFromLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE known_macs (
		id          INTEGER PRIMARY KEY,
		mac_address TEXT UNIQUE NOT NULL,
		last_seen   INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(
		"INSERT INTO known_macs (mac_address) VALUES ('aa:bb:cc:dd:ee:ff')"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	db, err := New(path)
	if err != nil {
		t.Fatalf("New on legacy database: %v", err)
	}
	defer db.Close()

	if got := loaded(t, db); !slices.Contains(got, "aa:bb:cc:dd:ee:ff") {
		t.Errorf("existing row lost during migration: %v", got)
	}

	// The new column must be usable straight away.
	future := time.Now().Add(time.Hour)
	if err := db.AllowMac("11:11:11:11:11:11", &future); err != nil {
		t.Errorf("AllowMac after migration: %v", err)
	}
}
