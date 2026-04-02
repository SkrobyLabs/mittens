package pool

import (
	"fmt"
	"regexp"
)

var (
	reID        = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	reSessionID = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	rePlanID    = regexp.MustCompile(`^[a-f0-9]{8}$`)
)

// ValidateID checks that id is a safe, non-empty identifier (alphanumeric, underscore, hyphen).
func ValidateID(id string) error {
	if !reID.MatchString(id) {
		return fmt.Errorf("invalid ID %q: must match %s", id, reID.String())
	}
	return nil
}

// ValidateSessionID checks that sessionID is a safe, non-empty identifier for
// pool labels, state directories, and session/container naming. Session IDs may
// include dots, underscores, and hyphens to match named team sessions such as
// "release.v1" or ".scratch", but path-like values remain disallowed.
func ValidateSessionID(sessionID string) error {
	if sessionID == "." || sessionID == ".." || !reSessionID.MatchString(sessionID) {
		return fmt.Errorf("invalid session ID %q: must match %s", sessionID, reSessionID.String())
	}
	return nil
}

// ValidatePlanID checks that planID matches the 8-char hex format produced by CreatePlan.
func ValidatePlanID(planID string) error {
	if !rePlanID.MatchString(planID) {
		return fmt.Errorf("invalid plan ID %q: must match %s", planID, rePlanID.String())
	}
	return nil
}
