package database

import (
	"slices"
	"testing"
	"time"
)

// Exporting a temporary allow would promote it to permanent on the next import,
// so only permanent entries are written out.
func TestPermanentMacsExcludesTemporary(t *testing.T) {
	db, _ := newTestDB(t)

	if err := db.AllowMac("aa:bb:cc:dd:ee:ff", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateKnownMacs("11:22:33:44:55:66"); err != nil {
		t.Fatal(err)
	}

	live := time.Now().Add(time.Hour)
	if err := db.AllowMac("22:22:22:22:22:22", &live); err != nil {
		t.Fatal(err)
	}
	lapsed := time.Now().Add(-time.Hour)
	if err := db.AllowMac("33:33:33:33:33:33", &lapsed); err != nil {
		t.Fatal(err)
	}

	macs, skipped, err := db.PermanentMacs()
	if err != nil {
		t.Fatalf("PermanentMacs: %v", err)
	}

	want := []string{"11:22:33:44:55:66", "aa:bb:cc:dd:ee:ff"}
	if !slices.Equal(macs, want) {
		t.Errorf("PermanentMacs returned %v, want %v", macs, want)
	}
	// Only the live temporary allow is reported; the lapsed one is on its way out.
	if skipped != 1 {
		t.Errorf("skippedTemporary = %d, want 1", skipped)
	}
}

func TestPermanentMacsSorted(t *testing.T) {
	db, _ := newTestDB(t)

	for _, mac := range []string{"ff:ff:ff:ff:ff:ff", "00:00:00:00:00:00", "aa:aa:aa:aa:aa:aa"} {
		if err := db.UpdateKnownMacs(mac); err != nil {
			t.Fatal(err)
		}
	}

	macs, _, err := db.PermanentMacs()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSorted(macs) {
		t.Errorf("PermanentMacs returned unsorted output: %v", macs)
	}
}

func TestPermanentMacsEmpty(t *testing.T) {
	db, _ := newTestDB(t)

	macs, skipped, err := db.PermanentMacs()
	if err != nil {
		t.Fatalf("PermanentMacs on empty database: %v", err)
	}
	if len(macs) != 0 || skipped != 0 {
		t.Errorf("got %v / %d, want empty", macs, skipped)
	}
}
