package channel

// Request is a message from a container to the UI.
type Request struct {
	ID        string                 `json:"id"`
	SessionID string                 `json:"sessionId"`
	Type      string                 `json:"type"` // "add-dir", "login", etc.
	Payload   map[string]interface{} `json:"payload"`
}

// Response is the UI's reply to a container request.
type Response struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}
