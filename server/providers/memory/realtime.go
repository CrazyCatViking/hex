package memory

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	hex "github.com/hex-platform/hex/server"
)

type Realtime struct {
	mu    sync.Mutex
	rooms map[string]map[*subscription]bool
}

type subscription struct {
	owner    *Realtime
	room     string
	messages chan json.RawMessage
}

func NewRealtime() *Realtime {
	return &Realtime{rooms: make(map[string]map[*subscription]bool)}
}

func (r *Realtime) Subscribe(ctx context.Context, room string) (hex.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	subscriber := &subscription{
		owner:    r,
		room:     room,
		messages: make(chan json.RawMessage, 64),
	}
	if r.rooms[room] == nil {
		r.rooms[room] = make(map[*subscription]bool)
	}
	r.rooms[room][subscriber] = true

	return subscriber, nil
}

func (s *subscription) Messages() <-chan json.RawMessage {
	return s.messages
}

func (s *subscription) Close() {
	r := s.owner
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.rooms[s.room][s] {
		delete(r.rooms[s.room], s)
		close(s.messages)
	}
	if len(r.rooms[s.room]) == 0 {
		delete(r.rooms, s.room)
	}
}

func (r *Realtime) Publish(ctx context.Context, room string, data json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for subscriber := range r.rooms[room] {
		select {
		case subscriber.messages <- slices.Clone(data):
		default:
			delete(r.rooms[room], subscriber)
			close(subscriber.messages)
		}
	}

	return nil
}
