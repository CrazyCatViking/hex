package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadEvent(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		event string
		data  string
	}{
		{"multiline CRLF", ": keep-alive\r\nid: ignored\r\nretry: 1000\r\nevent: delta\r\ndata: one\r\ndata: two\r\n\r\n", "delta", "one\ntwo"},
		{"empty data lines", "data:\ndata: middle\ndata:\n\n", "", "\nmiddle\n"},
		{"one leading space", "event: first\nevent: final\ndata:  spaced\n\n", "final", " spaced"},
		{"unterminated event", "event: done\ndata: final", "done", "final"},
		{"event without data", "event: ping\n\n", "ping", ""},
		{"empty data event", "data:\n\n", "", ""},
		{"blank lines and comments", "\n: ping\n\nunknown: field\n\ndata: ok\n\n", "", "ok"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := bufio.NewReaderSize(strings.NewReader(test.input), 16)
			event, data, err := ReadEvent(reader)
			if err != nil || event != test.event || data != test.data {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, nil)", event, data, err, test.event, test.data)
			}
			if _, _, err := ReadEvent(reader); !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF after the event, got %v", err)
			}
		})
	}
}

func TestReadEventLimit(t *testing.T) {
	// The limit includes fields, comments, and line endings, not just data.
	atLimit := "data: " + strings.Repeat("x", MaxEventBytes-len("data: \n\n")) + "\n\n"
	reader := bufio.NewReader(strings.NewReader(atLimit + atLimit))
	for range 2 {
		if _, _, err := ReadEvent(reader); err != nil {
			t.Fatalf("event at limit was rejected, or limit did not reset: %v", err)
		}
	}
	for _, input := range []string{
		"data: " + strings.Repeat("x", MaxEventBytes),
		strings.Repeat(": ignored\n", MaxEventBytes/len(": ignored\n")+1),
		strings.TrimSuffix(atLimit, "\n") + "x\n",
	} {
		if _, _, err := ReadEvent(bufio.NewReaderSize(strings.NewReader(input), 16)); err == nil || err.Error() != "server-sent event exceeds 8 MiB" {
			t.Fatalf("expected preserved size-limit error, got %v", err)
		}
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestReadEventErrorsAndEOF(t *testing.T) {
	failure := errors.New("transport failed")
	reader := bufio.NewReader(io.MultiReader(strings.NewReader("data: unfinished"), failingReader{err: failure}))
	if _, _, err := ReadEvent(reader); !errors.Is(err, failure) {
		t.Fatalf("lost transport error: %v", err)
	}
	for _, input := range []string{"", ": comment", "id: ignored\n\n"} {
		if _, _, err := ReadEvent(bufio.NewReader(strings.NewReader(input))); !errors.Is(err, io.EOF) {
			t.Fatalf("%q: expected EOF, got %v", input, err)
		}
	}
}
