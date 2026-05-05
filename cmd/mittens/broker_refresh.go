package main

import (
	"io"
	"net/http"
	"time"
)

// refreshCoordTimeout is how long a refresh coordinator holds the lock before
// it is considered stale and a new coordinator can be appointed.
const refreshCoordTimeout = 2 * time.Minute

// handleRefresh coordinates proactive token refresh across containers.
// The first container to POST becomes the coordinator and receives "refresh";
// subsequent posters receive "wait" until fresh creds arrive or the deadline expires.
func (b *HostBroker) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !b.requireMethod(w, r, http.MethodPost) {
		return
	}

	b.refreshMu.Lock()
	now := time.Now()
	inProgress := b.refreshInProgress && now.Before(b.refreshDeadline)
	if !inProgress {
		b.refreshInProgress = true
		b.refreshDeadline = now.Add(refreshCoordTimeout)
	}
	b.refreshMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if inProgress {
		b.blog("REFRESH -> wait (coordinator active until %s)", b.refreshDeadline.Format("15:04:05"))
		_, _ = io.WriteString(w, `{"action":"wait"}`)
		return
	}
	b.blog("REFRESH -> refresh (coordinator appointed)")
	_, _ = io.WriteString(w, `{"action":"refresh"}`)
}
