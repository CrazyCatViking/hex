package hex

import (
	"strings"
	"unicode"
)

// CSV quoting escapes separators, not spreadsheet formulas. Keep the original
// text but mark potentially executable cells as text before CSV serialization.
func spreadsheetText(value string) string {
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == '\uFEFF'
	})
	if strings.ContainsAny(value, "\t\r\n") || len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}
