package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/zsamuels28/unificlientalerts/internal/config"
	"github.com/zsamuels28/unificlientalerts/internal/unifi"
	_ "modernc.org/sqlite"
)

// Database manages the SQLite store of known MAC addresses.
type Database struct {
	db *sql.DB
}

func New(path string) (*Database, error) {
	// Ensure the directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	// Production SQLite settings: WAL mode for concurrent reads (if supported),
	// busy timeout to avoid "database is locked" errors, and foreign keys for data integrity.
	// Try WAL first; on network storage (Unraid, NAS), fall back to DELETE mode.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Printf("WAL mode not supported (network storage?); falling back to DELETE mode: %v", err)
	}

	pragmas := []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set %s: %w", p, err)
		}
	}

	// Limit to 2 connections: WAL mode allows 1 writer + 1 concurrent reader.
	db.SetMaxOpenConns(2)

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS known_macs (
		id          INTEGER PRIMARY KEY,
		mac_address TEXT UNIQUE NOT NULL,
		last_seen   INTEGER,
		expires_at  INTEGER
	)`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create table: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Database{db: db}, nil
}

// migrate brings databases created by earlier versions up to the current schema.
func migrate(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(known_macs)")
	if err != nil {
		return fmt.Errorf("failed to inspect schema: %w", err)
	}
	defer rows.Close()

	hasExpiresAt := false
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, ctype      string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("failed to read schema: %w", err)
		}
		if name == "expires_at" {
			hasExpiresAt = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to read schema: %w", err)
	}

	if !hasExpiresAt {
		if _, err := db.Exec("ALTER TABLE known_macs ADD COLUMN expires_at INTEGER"); err != nil {
			return fmt.Errorf("failed to add expires_at column: %w", err)
		}
		log.Printf("Database migrated: added expires_at column for temporary allows.")
	}
	return nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

// LoadKnownMacs returns the union of env-provided MACs and DB-stored MACs.
func (d *Database) LoadKnownMacs(envMacs []string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, mac := range envMacs {
		if mac != "" {
			seen[mac] = struct{}{}
		}
	}

	// Temporary allows that have lapsed are treated as unknown again, so the
	// device produces a fresh alert on the next check.
	rows, err := d.db.Query(
		"SELECT mac_address FROM known_macs WHERE expires_at IS NULL OR expires_at > ?",
		time.Now().Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var mac string
		if err := rows.Scan(&mac); err != nil {
			return nil, err
		}
		seen[mac] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]string, 0, len(seen))
	for mac := range seen {
		result = append(result, mac)
	}
	return result, nil
}

// UpdateKnownMacs inserts a new MAC/ID into the database (no-op if already present).
func (d *Database) UpdateKnownMacs(mac string) error {
	if mac == "" {
		return fmt.Errorf("cannot store empty MAC/identifier")
	}
	_, err := d.db.Exec("INSERT OR IGNORE INTO known_macs (mac_address) VALUES (?)", mac)
	return err
}

// AllowMac marks a MAC/identifier as known, overwriting any existing entry.
// A nil until allows it permanently; otherwise the entry lapses at until and
// the device alerts again. last_seen is cleared so an interactive allow is not
// immediately reaped by RemoveOldMacs.
func (d *Database) AllowMac(mac string, until *time.Time) error {
	if mac == "" {
		return fmt.Errorf("cannot allow empty MAC/identifier")
	}

	var expiresAt any
	if until != nil {
		expiresAt = until.Unix()
	}

	_, err := d.db.Exec(`
		INSERT INTO known_macs (mac_address, expires_at, last_seen) VALUES (?, ?, NULL)
		ON CONFLICT(mac_address) DO UPDATE SET expires_at = excluded.expires_at, last_seen = NULL`,
		mac, expiresAt)
	return err
}

// PurgeExpired deletes lapsed temporary allows and returns the MACs removed,
// so the caller can drop them from its in-memory known set.
func (d *Database) PurgeExpired() ([]string, error) {
	now := time.Now().Unix()

	rows, err := d.db.Query(
		"SELECT mac_address FROM known_macs WHERE expires_at IS NOT NULL AND expires_at <= ?", now)
	if err != nil {
		return nil, err
	}

	var expired []string
	for rows.Next() {
		var mac string
		if err := rows.Scan(&mac); err != nil {
			rows.Close()
			return nil, err
		}
		expired = append(expired, mac)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(expired) == 0 {
		return nil, nil
	}

	if _, err := d.db.Exec(
		"DELETE FROM known_macs WHERE expires_at IS NOT NULL AND expires_at <= ?", now); err != nil {
		return nil, err
	}
	return expired, nil
}

// RemoveOldMacs removes devices that have been absent longer than delay seconds.
// Devices that reappear have their last_seen timestamp cleared.
// Uses a transaction to batch all updates/deletes for consistency and performance.
func (d *Database) RemoveOldMacs(clients []unifi.NetworkClient, delay int64) error {
	currentMacs := make(map[string]struct{}, len(clients))
	for _, c := range clients {
		identifier := c.Identifier(true)
		if identifier != "" {
			currentMacs[identifier] = struct{}{}
		}
	}

	rows, err := d.db.Query("SELECT mac_address, last_seen, expires_at FROM known_macs")
	if err != nil {
		return err
	}

	type entry struct {
		mac       string
		lastSeen  sql.NullInt64
		expiresAt sql.NullInt64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.mac, &e.lastSeen, &e.expiresAt); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if len(entries) == 0 {
		return nil
	}

	tx, err := d.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is a no-op after commit

	now := time.Now().Unix()
	for _, e := range entries {
		// A live temporary allow outlasts absence: the user asked to stay quiet
		// about this device until it lapses, whether or not it stays connected.
		// PurgeExpired reaps it once the deadline passes.
		if e.expiresAt.Valid && e.expiresAt.Int64 > now {
			continue
		}

		if _, online := currentMacs[e.mac]; !online {
			if !e.lastSeen.Valid {
				if _, err := tx.Exec("UPDATE known_macs SET last_seen = ? WHERE mac_address = ?", now, e.mac); err != nil {
					return fmt.Errorf("failed to update last_seen for %s: %w", e.mac, err)
				}
			} else if e.lastSeen.Int64+delay < now {
				absentDuration := config.HumanDuration(now - e.lastSeen.Int64)
				log.Printf("Forgetting device %s (absent for %s)", e.mac, absentDuration)
				if _, err := tx.Exec("DELETE FROM known_macs WHERE mac_address = ?", e.mac); err != nil {
					return fmt.Errorf("failed to delete %s: %w", e.mac, err)
				}
			}
		} else {
			if e.lastSeen.Valid {
				if _, err := tx.Exec("UPDATE known_macs SET last_seen = NULL WHERE mac_address = ?", e.mac); err != nil {
					return fmt.Errorf("failed to clear last_seen for %s: %w", e.mac, err)
				}
			}
		}
	}

	return tx.Commit()
}
