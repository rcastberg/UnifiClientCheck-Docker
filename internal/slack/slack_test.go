package slack

import (
	"encoding/json"
	"testing"
)

func testClient() *Client {
	return New(Config{
		ChannelID: "C123",
		Durations: []Duration{
			{Label: "1h", Seconds: 3600},
			{Label: "6h", Seconds: 21600},
		},
	})
}

// The duration must be resolved from config rather than taken from the payload,
// so a forged button value cannot pick an arbitrary length.
func TestDurationForRejectsUnknownLabel(t *testing.T) {
	c := testClient()

	if secs, ok := c.durationFor("6h"); !ok || secs != 21600 {
		t.Errorf("durationFor(6h) = %d, %v; want 21600, true", secs, ok)
	}
	if _, ok := c.durationFor("9999w"); ok {
		t.Error("durationFor accepted a label that is not configured")
	}
	if _, ok := c.durationFor(""); ok {
		t.Error("durationFor accepted an empty label")
	}
}

func TestUserAllowed(t *testing.T) {
	open := New(Config{})
	if !open.userAllowed("U123") {
		t.Error("empty AllowedUsers should permit anyone")
	}

	restricted := New(Config{AllowedUsers: []string{"U111", "U222"}})
	if !restricted.userAllowed("U222") {
		t.Error("listed user should be permitted")
	}
	if restricted.userAllowed("U999") {
		t.Error("unlisted user should be rejected")
	}
}

func TestAlertBlocks(t *testing.T) {
	c := testClient()
	blocks := c.alertBlocks("aa:bb:cc:dd:ee:ff", "Device seen on network")

	if len(blocks) != 2 {
		t.Fatalf("expected a section and an actions block, got %d", len(blocks))
	}

	elements, ok := blocks[1]["elements"].([]map[string]any)
	if !ok {
		t.Fatalf("actions block has no elements")
	}
	// One button per duration, plus the permanent option.
	if len(elements) != 3 {
		t.Fatalf("expected 3 buttons, got %d", len(elements))
	}

	if got := elements[0]["value"]; got != "aa:bb:cc:dd:ee:ff|1h" {
		t.Errorf("first button value = %v, want aa:bb:cc:dd:ee:ff|1h", got)
	}
	if got := elements[2]["action_id"]; got != actionAllowPermanent {
		t.Errorf("last button action_id = %v, want %s", got, actionAllowPermanent)
	}
	if got := elements[2]["value"]; got != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("permanent button value = %v, want the bare MAC", got)
	}

	// Slack rejects malformed blocks, so the payload must marshal cleanly.
	if _, err := json.Marshal(blocks); err != nil {
		t.Errorf("blocks do not marshal to JSON: %v", err)
	}
}

// Slack sends the interaction wrapped in a Socket Mode envelope; the fields we
// depend on must survive decoding.
func TestEnvelopeDecoding(t *testing.T) {
	raw := `{
		"type": "interactive",
		"envelope_id": "env-1",
		"payload": {
			"type": "block_actions",
			"user": {"id": "U123", "username": "rene"},
			"response_url": "https://hooks.slack.com/actions/x",
			"actions": [{"action_id": "allow_temp_6h", "value": "aa:bb:cc:dd:ee:ff|6h"}]
		}
	}`

	var env envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if env.EnvelopeID != "env-1" {
		t.Errorf("EnvelopeID = %q, want env-1", env.EnvelopeID)
	}
	if env.Payload.User.ID != "U123" {
		t.Errorf("User.ID = %q, want U123", env.Payload.User.ID)
	}
	if len(env.Payload.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(env.Payload.Actions))
	}
	if env.Payload.Actions[0].Value != "aa:bb:cc:dd:ee:ff|6h" {
		t.Errorf("action value = %q", env.Payload.Actions[0].Value)
	}
}
