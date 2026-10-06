package openai

import (
	"bufio"

	"github.com/crazycatviking/hex/internal/sse"
)

const maxEventBytes = sse.MaxEventBytes

// readEvent reads one server-sent event, returning its name and the data
// lines joined by newlines. It returns io.EOF only when no event remains.
func readEvent(reader *bufio.Reader) (string, string, error) {
	return sse.ReadEvent(reader)
}
