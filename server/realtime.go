package hex

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	if !validateIdentifiers(w, r) {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	room := r.PathValue("site") + "/" + r.PathValue("channel")
	subscription, err := s.config.Realtime.Subscribe(ctx, room)
	if err != nil {
		writeServerError(w, err)
		return
	}
	defer subscription.Close()

	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		slog.Debug("WebSocket upgrade rejected", "error", err)
		return
	}
	defer func() {
		if err := connection.CloseNow(); err != nil {
			slog.Debug("close WebSocket transport", "error", err)
		}
	}()
	connection.SetReadLimit(64 << 10)

	go func() {
		defer cancel()
		s.receiveMessages(ctx, connection, room)
	}()

	streamMessages(ctx, connection, subscription)
}

func (s *Server) receiveMessages(ctx context.Context, connection *websocket.Conn, room string) {
	for {
		messageType, data, err := connection.Read(ctx)
		if err != nil {
			slog.Debug("WebSocket reader stopped", "room", room, "error", err)
			return
		}

		if messageType != websocket.MessageText || !json.Valid(data) {
			closeWebSocket(connection, websocket.StatusInvalidFramePayloadData, "expected JSON text")
			return
		}

		if err := s.config.Realtime.Publish(ctx, room, data); err != nil {
			slog.Error("publish WebSocket message", "room", room, "error", err)
			closeWebSocket(connection, websocket.StatusInternalError, "publish failed")
			return
		}
	}
}

func streamMessages(ctx context.Context, connection *websocket.Conn, subscription Subscription) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case data, ok := <-subscription.Messages():
			if !ok {
				closeWebSocket(connection, websocket.StatusTryAgainLater, "subscriber too slow")
				return
			}

			writeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := connection.Write(writeContext, websocket.MessageText, data)
			cancel()
			if err != nil {
				slog.Debug("WebSocket writer stopped", "error", err)
				return
			}

		case <-ticker.C:
			pingContext, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := connection.Ping(pingContext)
			cancel()
			if err != nil {
				slog.Debug("WebSocket ping failed", "error", err)
				return
			}
		}
	}
}

func closeWebSocket(connection *websocket.Conn, status websocket.StatusCode, reason string) {
	if err := connection.Close(status, reason); err != nil {
		slog.Debug("close WebSocket", "reason", reason, "error", err)
	}
}
