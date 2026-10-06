package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	hex "github.com/crazycatviking/hex/server"
)

func TestAutomationOutputEscapesTerminalControls(t *testing.T) {
	output := new(bytes.Buffer)
	app := &App{Out: output}
	attack := "\x1b]52;c;Y2xpcGJvYXJk\a\x9b31m\nspoofed\u202e"
	run := hex.AutomationRun{Automation: "demo", Trigger: hex.TriggerTest, Status: hex.RunSucceeded, StartedAt: time.Now(), FinishedAt: time.Now(), Logs: []hex.AutomationLog{{Message: attack}}, Operations: []hex.AutomationOperation{{Kind: "query", Target: attack, Status: hex.OperationFailed, Error: attack}}}
	if err := printRun(app, run); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, control := range []string{"\x1b", "\a", "\x9b", "\u202e"} {
		if strings.Contains(text, control) {
			t.Fatalf("terminal control survived: %q", text)
		}
	}
	if !strings.Contains(text, `\u001b`) || terminalText("Kari ✓") != "Kari ✓" {
		t.Fatalf("escaping changed readable text: %q", text)
	}
}
