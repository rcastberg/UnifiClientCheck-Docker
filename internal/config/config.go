package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	KnownMacs           []string
	NotificationService string
	AlwaysNotify        bool
	RememberNewDevices  bool
	RemoveOldDevices    bool
	RequireIP           bool
	RemoveDelay         int64
	DatabasePath        string
	WSEventDelay        int  // seconds to wait after a WS event before querying the API
	FallbackInterval    int  // seconds for fallback checks; -1 = disabled, default = 60
	Verbose             bool // if true, log diagnostic/polling details; default = false

	// Interactive Slack (NOTIFICATION_SERVICE=SlackInteractive)
	SlackBotToken     string
	SlackAppToken     string
	SlackChannelID    string
	SlackAllowedUsers []string
	SlackDurations    []AllowDuration
	SlackExportCmd    string
	SlackReloadCmd    string

	// EnvKnownMacs holds only the KNOWN_MACS entries, kept separate from
	// KnownMacs so a reload can re-read the file without duplicating them.
	EnvKnownMacs []string
	// KnownMacsFile is the path KNOWN_MACS_FILE was read from, kept so it can be
	// re-read on reload and so the export can warn when it would overwrite its
	// own input.
	KnownMacsFile  string
	MacsExportFile string
}

// AllowDuration is one temporary-allow option offered on an interactive alert.
type AllowDuration struct {
	Label   string
	Seconds int64
}

func Load() Config {
	cfg := Config{
		NotificationService: "Telegram",
		RememberNewDevices:  true,
		DatabasePath:        "/data/knownMacs.db",
		WSEventDelay:        3,
		FallbackInterval:    60, // default: fallback every 60 seconds
	}

	cfg.EnvKnownMacs = splitList(os.Getenv("KNOWN_MACS"))
	cfg.KnownMacsFile = os.Getenv("KNOWN_MACS_FILE")
	cfg.KnownMacs = cfg.KnownMacsSeed()

	if v := os.Getenv("NOTIFICATION_SERVICE"); v != "" {
		cfg.NotificationService = v
	}

	if v := os.Getenv("DATABASE_PATH"); v != "" {
		cfg.DatabasePath = v
	}

	cfg.AlwaysNotify = parseBool(os.Getenv("ALWAYS_NOTIFY"), false)
	cfg.RememberNewDevices = parseBool(os.Getenv("REMEMBER_NEW_DEVICES"), true)
	cfg.RemoveOldDevices = parseBool(os.Getenv("REMOVE_OLD_DEVICES"), false)
	cfg.RequireIP = parseBool(os.Getenv("REQUIRE_IP"), false)

	if v := os.Getenv("REMOVE_DELAY"); v != "" {
		if n, ok := parseDuration(v); ok {
			cfg.RemoveDelay = n
		} else {
			log.Printf("Warning: REMOVE_DELAY has invalid format %q, ignoring (using default: 0)", v)
		}
	}

	if v := os.Getenv("WS_EVENT_DELAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.WSEventDelay = n
		}
	}

	if v := os.Getenv("FALLBACK_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && (n == -1 || n > 0) {
			cfg.FallbackInterval = n
		}
	}

	cfg.Verbose = parseBool(os.Getenv("VERBOSE"), false)

	cfg.SlackBotToken = os.Getenv("SLACK_BOT_TOKEN")
	cfg.SlackAppToken = os.Getenv("SLACK_APP_TOKEN")
	cfg.SlackChannelID = os.Getenv("SLACK_CHANNEL_ID")
	cfg.SlackAllowedUsers = splitList(os.Getenv("SLACK_ALLOWED_USERS"))
	cfg.SlackDurations = parseAllowDurations(os.Getenv("SLACK_ALLOW_DURATIONS"))

	cfg.SlackExportCmd = "/writemacs"
	if v := strings.TrimSpace(os.Getenv("SLACK_EXPORT_COMMAND")); v != "" {
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		cfg.SlackExportCmd = v
	}

	cfg.SlackReloadCmd = "/reload"
	if v := strings.TrimSpace(os.Getenv("SLACK_RELOAD_COMMAND")); v != "" {
		if !strings.HasPrefix(v, "/") {
			v = "/" + v
		}
		cfg.SlackReloadCmd = v
	}

	cfg.MacsExportFile = "/data/known_macs.txt"
	if v := strings.TrimSpace(os.Getenv("MACS_EXPORT_FILE")); v != "" {
		cfg.MacsExportFile = v
	}

	return cfg
}

// KnownMacsSeed returns the configured known MACs: the KNOWN_MACS entries plus
// the contents of KNOWN_MACS_FILE, which is re-read on each call so edits to the
// file are picked up without restarting.
func (c Config) KnownMacsSeed() []string {
	seed := append([]string(nil), c.EnvKnownMacs...)
	if c.KnownMacsFile != "" {
		seed = append(seed, loadMacsFromFile(c.KnownMacsFile)...)
	}
	return seed
}

// splitList parses a comma-separated environment value, dropping blanks.
func splitList(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// defaultAllowDurations is used when SLACK_ALLOW_DURATIONS is unset.
const defaultAllowDurations = "1h,6h,24h"

// parseAllowDurations builds the temporary-allow buttons from a comma-separated
// list such as "1h,6h,24h". Unparseable entries are warned about and skipped;
// Slack rejects a message with more than five buttons in one block, so the list
// is capped at four to leave room for the permanent option.
func parseAllowDurations(v string) []AllowDuration {
	if strings.TrimSpace(v) == "" {
		v = defaultAllowDurations
	}

	var out []AllowDuration
	for _, label := range splitList(v) {
		seconds, ok := parseDuration(label)
		if !ok || seconds <= 0 {
			log.Printf("Warning: SLACK_ALLOW_DURATIONS entry %q is not a valid duration, ignoring", label)
			continue
		}
		out = append(out, AllowDuration{Label: label, Seconds: seconds})
		if len(out) == 4 {
			log.Printf("Warning: SLACK_ALLOW_DURATIONS is capped at 4 entries; ignoring the rest")
			break
		}
	}
	return out
}

// macPattern matches a MAC address in either colon- or hyphen-separated form.
var macPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?:[:-][0-9a-f]{2}){5}\b`)

// loadMacsFromFile scans path for MAC addresses and returns them normalised to
// the lowercase, colon-separated form the UniFi API reports, so that entries
// written with hyphens or in uppercase still match. Anything else in the file
// is ignored, so comments and surrounding prose are safe.
// A missing or unreadable file is logged and treated as empty rather than fatal.
func loadMacsFromFile(path string) []string {
	contents, err := os.ReadFile(path)
	if err != nil {
		log.Printf("Warning: could not read KNOWN_MACS_FILE %q: %v", path, err)
		return nil
	}

	matches := macPattern.FindAllString(string(contents), -1)
	macs := make([]string, 0, len(matches))
	for _, mac := range matches {
		macs = append(macs, strings.ToLower(strings.ReplaceAll(mac, "-", ":")))
	}

	log.Printf("Loaded %d MAC addresses from %s", len(macs), path)
	return macs
}

// WriteMacsFile writes macs to path in the format loadMacsFromFile reads, so an
// export can be fed straight back in via KNOWN_MACS_FILE. Any existing file is
// replaced.
//
// The write goes to a temporary file in the same directory and is then renamed
// over the target, so an interrupted run cannot leave a half-written list in
// place of a good one.
func WriteMacsFile(path string, macs []string) error {
	var buf strings.Builder
	fmt.Fprintf(&buf, "# Known MAC addresses exported by UniFiClientAlerts\n")
	fmt.Fprintf(&buf, "# %s — %d entries\n", time.Now().Format(time.RFC3339), len(macs))
	for _, mac := range macs {
		buf.WriteString(mac)
		buf.WriteByte('\n')
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, ".macs-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.WriteString(buf.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("failed to write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, 0640); err != nil {
		return fmt.Errorf("failed to set permissions on %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to replace %s: %w", path, err)
	}
	return nil
}

func parseBool(s string, defaultVal bool) bool {
	if s == "" {
		return defaultVal
	}
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "true" || s == "1" || s == "yes"
}

// parseDuration converts a duration string to seconds.
// Accepts raw integer (e.g., "86400") or suffixed values (e.g., "30s", "24h", "7d", "2w").
// Returns (seconds, success).
func parseDuration(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, true
	}

	// Try raw integer first (backward compat for raw seconds)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
		return n, true
	}

	// Try suffix-based parsing
	suffixes := map[string]int64{"s": 1, "m": 60, "h": 3600, "d": 86400, "w": 604800}
	for suffix, mult := range suffixes {
		if strings.HasSuffix(s, suffix) {
			if n, err := strconv.ParseInt(s[:len(s)-1], 10, 64); err == nil && n >= 0 {
				return n * mult, true
			}
		}
	}

	return 0, false
}

// HumanDuration formats a duration in seconds to a human-readable string.
// Examples: "30s", "2m", "1h", "7d", "2w".
func HumanDuration(seconds int64) string {
	if seconds <= 0 {
		return "0s"
	}

	type unit struct {
		name     string
		duration int64
	}
	units := []unit{
		{"w", 604800},
		{"d", 86400},
		{"h", 3600},
		{"m", 60},
		{"s", 1},
	}

	var parts []string
	remaining := seconds
	for _, u := range units {
		if remaining >= u.duration {
			count := remaining / u.duration
			remaining = remaining % u.duration
			parts = append(parts, strconv.FormatInt(count, 10)+u.name)
		}
	}

	if len(parts) == 0 {
		return "0s"
	}

	if len(parts) == 1 {
		return parts[0]
	}

	// Return top 2 units for readability
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, " ")
}
