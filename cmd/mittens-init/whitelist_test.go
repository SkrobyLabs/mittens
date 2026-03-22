package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestDomainWhitelistExact(t *testing.T) {
	wl := newDomainWhitelist([]string{"api.github.com", "pypi.org"})

	if !wl.allowed("api.github.com") {
		t.Error("expected api.github.com to be allowed")
	}
	if !wl.allowed("pypi.org") {
		t.Error("expected pypi.org to be allowed")
	}
	if wl.allowed("evil.com") {
		t.Error("expected evil.com to be denied")
	}
}

func TestDomainWhitelistCaseInsensitive(t *testing.T) {
	wl := newDomainWhitelist([]string{"API.GitHub.Com"})
	if !wl.allowed("api.github.com") {
		t.Error("expected case-insensitive match")
	}
}

func TestDomainWhitelistWildcard(t *testing.T) {
	wl := newDomainWhitelist([]string{".amazonaws.com"})

	if !wl.allowed("s3.amazonaws.com") {
		t.Error("expected s3.amazonaws.com to match .amazonaws.com")
	}
	if !wl.allowed("sts.us-east-1.amazonaws.com") {
		t.Error("expected deep subdomain to match .amazonaws.com")
	}
	if !wl.allowed("amazonaws.com") {
		t.Error("expected bare domain to match .amazonaws.com")
	}
	if wl.allowed("notamazonaws.com") {
		t.Error("notamazonaws.com should not match")
	}
}

func TestDomainWhitelistEmpty(t *testing.T) {
	wl := newDomainWhitelist(nil)
	if wl.allowed("anything.com") {
		t.Error("empty whitelist should deny everything")
	}
}

func TestDomainWhitelistCount(t *testing.T) {
	wl := newDomainWhitelist([]string{"a.com", "b.com", ".c.com"})
	// "a.com", "b.com", "c.com" (bare from wildcard) = 3 exact + 1 suffix
	if wl.count() < 3 {
		t.Errorf("expected at least 3 entries, got %d", wl.count())
	}
}

func TestParseWhitelistReader(t *testing.T) {
	input := `# Comment line
api.github.com    # inline comment
pypi.org
   .amazonaws.com

# Another comment
registry.npmjs.org`

	scanner := bufio.NewScanner(strings.NewReader(input))
	domains := parseWhitelistReader(scanner)

	expected := []string{"api.github.com", "pypi.org", ".amazonaws.com", "registry.npmjs.org"}
	if len(domains) != len(expected) {
		t.Fatalf("expected %d domains, got %d: %v", len(expected), len(domains), domains)
	}
	for i, d := range domains {
		if d != expected[i] {
			t.Errorf("domain[%d] = %q, want %q", i, d, expected[i])
		}
	}
}

func TestDedup(t *testing.T) {
	input := []string{"b", "a", "b", "c", "a"}
	result := dedup(input)
	if len(result) != 3 {
		t.Fatalf("expected 3 unique items, got %d", len(result))
	}
	if result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("expected [a b c], got %v", result)
	}
}
