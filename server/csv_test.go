package hex

import "testing"

func TestSpreadsheetText(t *testing.T) {
	for _, value := range []string{"=1+1", "+SUM(A1)", "-1+1", "@SUM(A1)", " \t=1+1", "\r=1", "\n=1", "\u00a0=1", "\uFEFF=1", "\x00=1", "normal\n=1"} {
		if got := spreadsheetText(value); got != "'"+value {
			t.Errorf("unsafe cell %q became %q", value, got)
		}
	}
	for _, value := range []string{"", "Alice", "user:alice", "ticket:123", `{"formula":"=1+1"}`, "100"} {
		if got := spreadsheetText(value); got != value {
			t.Errorf("safe cell %q changed to %q", value, got)
		}
	}
}
