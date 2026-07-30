package slack

import (
	"encoding/json"
	"testing"
)

func commandClient() *Client {
	return New(Config{
		AllowedUsers:  []string{"U111"},
		ExportCommand: "/writemacs",
		ReloadCommand: "/reload",
	})
}

func payloadFor(command, userID string) interactionPayload {
	return interactionPayload{
		Type:     "slash_commands",
		Command:  command,
		UserID:   userID,
		UserName: "rene",
	}
}

func TestSlashCommandRouting(t *testing.T) {
	cases := []struct {
		command string
		want    CommandKind
	}{
		{"/writemacs", CommandExport},
		{"/reload", CommandReload},
		// Slack lowercases commands, but match defensively either way.
		{"/RELOAD", CommandReload},
	}

	for _, c := range cases {
		ch := make(chan Command, 1)
		commandClient().handleSlashCommand(payloadFor(c.command, "U111"), ch)
		select {
		case got := <-ch:
			if got.Kind != c.want {
				t.Errorf("%s routed to kind %d, want %d", c.command, got.Kind, c.want)
			}
			if got.User != "rene" {
				t.Errorf("%s user = %q, want rene", c.command, got.User)
			}
		default:
			t.Errorf("%s produced no command", c.command)
		}
	}
}

func TestUnknownSlashCommandIgnored(t *testing.T) {
	ch := make(chan Command, 1)
	commandClient().handleSlashCommand(payloadFor("/somethingelse", "U111"), ch)

	select {
	case got := <-ch:
		t.Errorf("unknown command produced %+v, want nothing", got)
	default:
	}
}

// An unauthorised user must not be able to reload or export, since either
// changes which devices are alerted on.
func TestSlashCommandRejectsUnauthorisedUser(t *testing.T) {
	for _, cmd := range []string{"/writemacs", "/reload"} {
		ch := make(chan Command, 1)
		commandClient().handleSlashCommand(payloadFor(cmd, "U999"), ch)

		select {
		case got := <-ch:
			t.Errorf("%s from unauthorised user produced %+v, want nothing", cmd, got)
		default:
		}
	}
}

func TestSlashCommandOpenWhenNoAllowlist(t *testing.T) {
	c := New(Config{ExportCommand: "/writemacs", ReloadCommand: "/reload"})
	ch := make(chan Command, 1)
	c.handleSlashCommand(payloadFor("/reload", "U999"), ch)

	select {
	case got := <-ch:
		if got.Kind != CommandReload {
			t.Errorf("kind = %d, want CommandReload", got.Kind)
		}
	default:
		t.Error("expected the command to be accepted when no allowlist is set")
	}
}

// "/reload fresh" must carry its argument through, since that is what decides
// whether stored allows are discarded.
func TestSlashCommandCarriesArgument(t *testing.T) {
	cases := map[string]string{
		"fresh":     "fresh",
		"  fresh  ": "fresh",
		"":          "",
	}

	for text, want := range cases {
		ch := make(chan Command, 1)
		p := payloadFor("/reload", "U111")
		p.Text = text
		commandClient().handleSlashCommand(p, ch)

		select {
		case got := <-ch:
			if got.Arg != want {
				t.Errorf("text %q produced Arg %q, want %q", text, got.Arg, want)
			}
		default:
			t.Errorf("text %q produced no command", text)
		}
	}
}

// The argument arrives in the slash_commands payload as "text".
func TestSlashCommandPayloadDecoding(t *testing.T) {
	raw := `{
		"type": "slash_commands",
		"command": "/reload",
		"text": "fresh",
		"user_id": "U111",
		"user_name": "rene",
		"response_url": "https://hooks.slack.com/commands/x"
	}`

	var p interactionPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Command != "/reload" || p.Text != "fresh" || p.UserID != "U111" {
		t.Errorf("decoded %+v", p)
	}
}
