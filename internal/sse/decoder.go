// Package sse decodes the bounded server-sent events used by AI providers.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const MaxEventBytes = 8 << 20

// ReadEvent reads an event name and newline-joined data fields. Comments and
// unknown fields are ignored. A final unterminated event is returned before
// io.EOF, which is returned only when no event remains.
func ReadEvent(reader *bufio.Reader) (string, string, error) {
	var name string
	var data strings.Builder
	hasField := false
	hasData := false
	size := 0
	for {
		line, err := readLine(reader, MaxEventBytes-size)
		size += len(line)
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
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
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

// ReadSlice bounds allocation even when a peer sends an oversized line
// without a newline.
func readLine(reader *bufio.Reader, remaining int) (string, error) {
	var line strings.Builder
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > remaining-line.Len() {
			return "", errors.New("server-sent event exceeds 8 MiB")
		}
		line.Write(fragment)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line.String(), err
		}
	}
}
