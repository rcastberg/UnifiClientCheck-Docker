package slack

import "testing"

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
