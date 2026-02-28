package ws

import "encoding/json"

// MessageType identifies the type of WebSocket control message.
type MessageType string

const (
	// Client → Server
	MsgInput  MessageType = "input"
	MsgResize MessageType = "resize"

	// Server → Client
	MsgScrollback MessageType = "scrollback"
	MsgState      MessageType = "state"
	MsgExit       MessageType = "exit"
)

// Message is a JSON control message sent over WebSocket text frames.
type Message struct {
	Type MessageType     `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`

	// Resize fields.
	Rows int `json:"rows,omitempty"`
	Cols int `json:"cols,omitempty"`

	// State field.
	State string `json:"state,omitempty"`

	// Exit field.
	Code int `json:"code,omitempty"`
}

// InputMessage is a client→server input message.
type InputMessage struct {
	Type MessageType `json:"type"`
	Data string      `json:"data"` // base64 encoded
}

// ResizeMessage is a client→server resize message.
type ResizeMessage struct {
	Type MessageType `json:"type"`
	Rows int         `json:"rows"`
	Cols int         `json:"cols"`
}
