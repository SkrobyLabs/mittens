package pool

import (
	"fmt"
	"regexp"
)

var (
	reID     = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	rePlanID = regexp.MustCompile(`^[a-f0-9]{8}$`)
)

// ValidateID checks that id is a safe, non-empty identifier (alphanumeric, underscore, hyphen).
func ValidateID(id string) error {
	if !reID.MatchString(id) {
		return fmt.Errorf("invalid ID %q: must match %s", id, reID.String())
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
