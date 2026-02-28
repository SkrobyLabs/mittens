package api

import (
	"encoding/json"
	"net/http"

	"github.com/Skroby/mittens/ui/internal/channel"
)

// ChannelHandler handles channel response endpoints.
type ChannelHandler struct {
	Channel *channel.Manager
}

// RespondRequest is the JSON body for responding to a channel request.
type RespondRequest struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

// Respond handles approval/denial of a channel request.
func (h *ChannelHandler) Respond(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestId")

	var req RespondRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	resp := &channel.Response{
		ID:       requestID,
		Approved: req.Approved,
		Reason:   req.Reason,
	}

	if err := h.Channel.Respond(requestID, resp); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
