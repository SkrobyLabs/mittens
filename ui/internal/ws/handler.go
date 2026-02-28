package ws

import (
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"

	"github.com/Skroby/mittens/ui/internal/session"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler handles WebSocket connections for terminal I/O.
type Handler struct {
	Sessions *session.Manager
	Hubs     *HubManager
}

// ServeWS upgrades an HTTP connection to WebSocket and streams terminal I/O.
func (h *Handler) ServeWS(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, ok := h.Sessions.Get(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}

	hub := h.Hubs.GetOrCreate(sessionID)
	hub.Add(conn)

	// Send scrollback on connect.
	scrollback := sess.Scrollback()
	if len(scrollback) > 0 {
		_ = conn.WriteMessage(websocket.BinaryMessage, scrollback)
	}

	// Send current state.
	stateMsg := Message{Type: MsgState, State: string(sess.State)}
	if data, err := json.Marshal(stateMsg); err == nil {
		_ = conn.WriteMessage(websocket.TextMessage, data)
	}

	// Read loop for client input.
	go func() {
		defer func() {
			hub.Remove(conn)
			conn.Close()
		}()

		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				return
			}

			if msgType == websocket.TextMessage {
				var msg Message
				if err := json.Unmarshal(data, &msg); err != nil {
					continue
				}

				switch msg.Type {
				case MsgInput:
					var input InputMessage
					if err := json.Unmarshal(data, &input); err != nil {
						continue
					}
					decoded, err := base64.StdEncoding.DecodeString(input.Data)
					if err != nil {
						continue
					}
					_ = h.Sessions.WriteInput(sessionID, decoded)

				case MsgResize:
					var resize ResizeMessage
					if err := json.Unmarshal(data, &resize); err != nil {
						continue
					}
					_ = h.Sessions.Resize(sessionID, uint16(resize.Rows), uint16(resize.Cols))
				}
			}
		}
	}()
}
