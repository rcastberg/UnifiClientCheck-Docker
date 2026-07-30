// Package slack implements the interactive Slack notifier.
//
// Unlike the plain "Slack" notification service, which posts to an incoming
// webhook and is outbound-only, this package uses a Slack app so alerts can
// carry buttons. Clicking one suppresses further alerts for that device, either
// permanently or until a deadline.
//
// The connection uses Socket Mode: the app dials out to Slack over a WebSocket
// and receives interactions on it. Nothing listens on an inbound port, so the
// container needs no public exposure, port forwarding or TLS certificate.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zsamuels28/unificlientalerts/internal/unifi"
)

const (
	connectionsOpenURL = "https://slack.com/api/apps.connections.open"
	postMessageURL     = "https://slack.com/api/chat.postMessage"

	// actionAllowPermanent and actionAllowTemp identify the alert buttons.
	actionAllowPermanent = "allow_permanent"
	actionAllowTemp      = "allow_temp"

	// reconnectDelay is the pause before redialling after the socket drops.
	reconnectDelay = 5 * time.Second

	// writeTimeout bounds a single WebSocket write (acks are tiny).
	writeTimeout = 10 * time.Second
)

// Config holds the settings for the interactive Slack notifier.
type Config struct {
	// BotToken is the xoxb- token used to post messages.
	BotToken string
	// AppToken is the xapp- app-level token used to open the Socket Mode
	// connection. It requires the connections:write scope.
	AppToken string
	// ChannelID is the channel alerts are posted to.
	ChannelID string
	// AllowedUsers restricts who may press the buttons, by Slack user ID.
	// Empty means anyone who can see the message may act on it.
	AllowedUsers []string
	// Durations are the temporary-allow options offered, as parsed seconds
	// paired with the label to show on the button.
	Durations []Duration
	// ExportCommand is the slash command that triggers a MAC export,
	// e.g. "/writemacs".
	ExportCommand string
}

// Duration is one temporary-allow button.
type Duration struct {
	Label   string
	Seconds int64
}

// Decision is an allow instruction produced by a button press. Until is nil for
// a permanent allow.
type Decision struct {
	MAC   string
	Until *time.Time
	User  string
}

// ExportRequest is a request to write the known MACs to disk, produced by the
// export slash command. ResponseURL is where the outcome should be reported.
type ExportRequest struct {
	User        string
	ResponseURL string
}

// Client posts interactive alerts and listens for the resulting button presses.
type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 15 * time.Second},
	}
}

// PostAlert posts a device alert carrying the allow buttons.
func (c *Client) PostAlert(client *unifi.NetworkClient, text string) error {
	identifier := client.Identifier(true)
	if identifier == "" {
		return fmt.Errorf("cannot post alert for a device with no identifier")
	}

	payload := map[string]any{
		"channel": c.cfg.ChannelID,
		"text":    text, // fallback for notifications and unsupported clients
		"blocks":  c.alertBlocks(identifier, text),
	}

	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := c.post(postMessageURL, c.cfg.BotToken, payload, &res); err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("chat.postMessage failed: %s", res.Error)
	}
	return nil
}

// alertBlocks renders the alert text plus one button per configured duration
// and a permanent option.
func (c *Client) alertBlocks(identifier, text string) []map[string]any {
	elements := make([]map[string]any, 0, len(c.cfg.Durations)+1)
	for _, d := range c.cfg.Durations {
		elements = append(elements, map[string]any{
			"type":      "button",
			"text":      map[string]any{"type": "plain_text", "text": "Allow " + d.Label},
			"action_id": actionAllowTemp + "_" + d.Label,
			"value":     identifier + "|" + d.Label,
		})
	}
	elements = append(elements, map[string]any{
		"type":      "button",
		"text":      map[string]any{"type": "plain_text", "text": "Allow permanently"},
		"action_id": actionAllowPermanent,
		"style":     "primary",
		"value":     identifier,
	})

	return []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		{"type": "actions", "elements": elements},
	}
}

// Listen maintains the Socket Mode connection, emitting a Decision for each
// accepted button press and an ExportRequest for each accepted export command.
// It returns only when ctx is cancelled; connection failures are logged and
// retried.
func (c *Client) Listen(ctx context.Context, decisions chan<- Decision, exports chan<- ExportRequest) {
	for {
		if err := c.listenOnce(ctx, decisions, exports); err != nil && ctx.Err() == nil {
			log.Printf("Slack Socket Mode connection ended: %v; reconnecting in %s", err, reconnectDelay)
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(reconnectDelay):
		}
	}
}

// listenOnce opens a single Socket Mode connection and pumps it until it fails.
func (c *Client) listenOnce(ctx context.Context, decisions chan<- Decision, exports chan<- ExportRequest) error {
	wssURL, err := c.openConnection()
	if err != nil {
		return err
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wssURL, nil)
	if err != nil {
		return fmt.Errorf("failed to dial Socket Mode: %w", err)
	}
	defer conn.Close()

	// Unblock the blocking ReadJSON below as soon as shutdown starts.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	log.Printf("Slack Socket Mode connected; listening for button presses.")

	for {
		var env envelope
		if err := conn.ReadJSON(&env); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("failed to read Socket Mode message: %w", err)
		}

		switch env.Type {
		case "hello":
			continue
		case "disconnect":
			// Slack asks the client to reconnect, e.g. before maintenance.
			return fmt.Errorf("slack asked us to disconnect (%s)", env.Reason)
		}

		// Every envelope other than hello must be acknowledged, otherwise Slack
		// retries it and the user sees a timeout in the client.
		if env.EnvelopeID != "" {
			if err := c.ack(conn, env.EnvelopeID); err != nil {
				return err
			}
		}

		switch env.Type {
		case "interactive":
			c.handleInteraction(env.Payload, decisions)
		case "slash_commands":
			c.handleSlashCommand(env.Payload, exports)
		}
	}
}

// handleSlashCommand validates the export command and forwards it for the main
// loop to carry out, since that goroutine owns the database and the known set.
func (c *Client) handleSlashCommand(payload interactionPayload, exports chan<- ExportRequest) {
	if !strings.EqualFold(payload.Command, c.cfg.ExportCommand) {
		log.Printf("Ignoring unknown slash command %q.", payload.Command)
		return
	}

	user := payload.UserName
	if user == "" {
		user = payload.UserID
	}

	if !c.userAllowed(payload.UserID) {
		log.Printf("Ignoring %s from unauthorised user %s (%s).", payload.Command, user, payload.UserID)
		c.Reply(payload.ResponseURL, ":no_entry: You are not permitted to export the known-device list.")
		return
	}

	exports <- ExportRequest{User: user, ResponseURL: payload.ResponseURL}
}

// openConnection exchanges the app-level token for a single-use WebSocket URL.
func (c *Client) openConnection() (string, error) {
	var res struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := c.post(connectionsOpenURL, c.cfg.AppToken, nil, &res); err != nil {
		return "", err
	}
	if !res.OK {
		return "", fmt.Errorf("apps.connections.open failed: %s", res.Error)
	}
	return res.URL, nil
}

func (c *Client) ack(conn *websocket.Conn, envelopeID string) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	if err := conn.WriteJSON(map[string]string{"envelope_id": envelopeID}); err != nil {
		return fmt.Errorf("failed to acknowledge envelope: %w", err)
	}
	return nil
}

// handleInteraction validates a button press and emits the resulting Decision.
func (c *Client) handleInteraction(payload interactionPayload, decisions chan<- Decision) {
	if payload.Type != "block_actions" || len(payload.Actions) == 0 {
		return
	}
	action := payload.Actions[0]

	user := payload.User.Username
	if user == "" {
		user = payload.User.ID
	}

	if !c.userAllowed(payload.User.ID) {
		log.Printf("Ignoring Slack action from unauthorised user %s (%s).", user, payload.User.ID)
		c.respond(payload.ResponseURL, fmt.Sprintf(
			":no_entry: <@%s> is not permitted to change device rules.", payload.User.ID))
		return
	}

	mac, label, _ := strings.Cut(action.Value, "|")
	if mac == "" {
		log.Printf("Ignoring Slack action with no MAC in its value.")
		return
	}

	var (
		until   *time.Time
		summary string
	)
	switch {
	case action.ActionID == actionAllowPermanent:
		summary = fmt.Sprintf(":white_check_mark: `%s` allowed permanently by <@%s>.", mac, payload.User.ID)
	case strings.HasPrefix(action.ActionID, actionAllowTemp):
		seconds, ok := c.durationFor(label)
		if !ok {
			log.Printf("Ignoring Slack action with unknown duration %q.", label)
			return
		}
		deadline := time.Now().Add(time.Duration(seconds) * time.Second)
		until = &deadline
		summary = fmt.Sprintf(":hourglass_flowing_sand: `%s` allowed for %s by <@%s> (until %s).",
			mac, label, payload.User.ID, deadline.Format("15:04 MST"))
	default:
		return
	}

	decisions <- Decision{MAC: mac, Until: until, User: user}
	c.respond(payload.ResponseURL, summary)
}

// userAllowed reports whether the given Slack user ID may press the buttons.
func (c *Client) userAllowed(userID string) bool {
	if len(c.cfg.AllowedUsers) == 0 {
		return true
	}
	for _, u := range c.cfg.AllowedUsers {
		if u == userID {
			return true
		}
	}
	return false
}

// durationFor resolves a button label back to its configured length. Resolving
// against the config rather than trusting the payload keeps a forged value from
// choosing an arbitrary duration.
func (c *Client) durationFor(label string) (int64, bool) {
	for _, d := range c.cfg.Durations {
		if d.Label == label {
			return d.Seconds, true
		}
	}
	return 0, false
}

// respond replaces the original alert with the outcome, so the buttons cannot
// be pressed twice and the channel records who decided what.
func (c *Client) respond(responseURL, text string) {
	c.sendResponse(responseURL, map[string]any{
		"replace_original": true,
		"text":             text,
		"blocks": []map[string]any{
			{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		},
	})
}

// Reply answers a slash command privately, visible only to the person who ran
// it. There is no original message to replace in that case.
func (c *Client) Reply(responseURL, text string) {
	c.sendResponse(responseURL, map[string]any{
		"response_type": "ephemeral",
		"text":          text,
	})
}

func (c *Client) sendResponse(responseURL string, payload map[string]any) {
	if responseURL == "" {
		return
	}
	if err := c.post(responseURL, "", payload, nil); err != nil {
		log.Printf("Failed to send Slack response: %v", err)
	}
}

// post sends a JSON request, optionally bearer-authenticated, and decodes the
// response into out when out is non-nil.
func (c *Client) post(url, token string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to marshal slack request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("slack returned HTTP %d", resp.StatusCode)
		}
		return nil
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// envelope is a Socket Mode frame.
type envelope struct {
	Type       string             `json:"type"`
	EnvelopeID string             `json:"envelope_id"`
	Reason     string             `json:"reason"`
	Payload    interactionPayload `json:"payload"`
}

// interactionPayload covers the subset of fields we use from both payload
// shapes Slack delivers: block_actions (nested User, Actions) and
// slash_commands (flat UserID, UserName, Command). The two sets do not
// collide, so one struct decodes either without ambiguity.
type interactionPayload struct {
	Type string `json:"type"`
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	ResponseURL string `json:"response_url"`
	Actions     []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`

	// slash_commands only
	Command  string `json:"command"`
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
}
