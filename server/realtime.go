package hex

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	if !identifiers(w, r) {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	room := r.PathValue("site") + "/" + r.PathValue("channel")
	sub, err := s.config.Realtime.Subscribe(ctx, room)
	if err != nil {
		serverError(w, err)
		return
	}
	defer sub.Close()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 10)
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ != websocket.MessageText || !json.Valid(data) {
				conn.Close(websocket.StatusInvalidFramePayloadData, "expected JSON text")
				return
			}
			if err := s.config.Realtime.Publish(ctx, room, data); err != nil {
				conn.Close(websocket.StatusInternalError, "publish failed")
				return
			}
		}
	}()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-sub.Messages():
			if !ok {
				conn.Close(websocket.StatusTryAgainLater, "subscriber too slow")
				return
			}
			writeCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, data)
			done()
			if err != nil {
				return
			}
		case <-ticker.C:
			pingCtx, done := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			done()
			if err != nil {
				return
			}
		}
	}
}
