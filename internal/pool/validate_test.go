package pool

import "testing"

func TestValidateID(t *testing.T) {
	valid := []string{"w-1", "worker_2", "abc-def-123", "A", "a1B2"}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) unexpected error: %v", id, err)
		}
	}

	invalid := []string{
		"",
		"../etc/passwd",
		"/absolute",
		"has space",
		"semi;colon",
		"dot.dot",
		"new\nline",
		"tab\there",
		"a/b",
		"back\\slash",
	}
	for _, id := range invalid {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) expected error, got nil", id)
		}
	}
}

func TestValidatePlanID(t *testing.T) {
	valid := []string{"abcdef01", "12345678", "0a1b2c3d"}
	for _, id := range valid {
		if err := ValidatePlanID(id); err != nil {
			t.Errorf("ValidatePlanID(%q) unexpected error: %v", id, err)
		}
	}

	invalid := []string{
		"",
		"ABCDEF01",         // uppercase hex not allowed
		"abcdefg1",         // 'g' is not hex
		"abcdef0",          // too short (7 chars)
		"abcdef012",        // too long (9 chars)
		"../abcd",          // path traversal
		"/abcdef0",         // absolute path
		"abcd ef0",         // space
		"abcdef\x00",       // null byte
		"w-1",              // general ID, not plan format
	}
	for _, id := range invalid {
		if err := ValidatePlanID(id); err == nil {
			t.Errorf("ValidatePlanID(%q) expected error, got nil", id)
		}
	}
}
