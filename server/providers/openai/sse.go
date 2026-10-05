package openai

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const maxEventBytes = 8 << 20

// readEvent reads one server-sent event, returning its name and the data
// lines joined by newlines. It returns io.EOF only when no event remains.
func readEvent(reader *bufio.Reader) (string, string, error) {
	var name string
	var data strings.Builder
	hasField := false
	size := 0
	for {
		line, err := reader.ReadString('\n')
		size += len(line)
		if size > maxEventBytes {
			return "", "", errors.New("server-sent event exceeds 8 MiB")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if hasField {
				return name, data.String(), nil
			}
			if err != nil {
				return "", "", io.EOF
			}
			continue
		}

		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
			hasField = true
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasField = true
		}
		if err != nil {
			if hasField {
				return name, data.String(), nil
			}
			return "", "", io.EOF
		}
	}
}
