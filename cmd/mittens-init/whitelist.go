package main

import (
	"bufio"
	"os"
	"sort"
	"strings"
)

// domainWhitelist is a set of allowed domain names with support for
// prefix-dot wildcards (e.g. ".amazonaws.com" matches any subdomain).
type domainWhitelist struct {
	exact    map[string]bool // exact domain matches
	suffixes []string        // wildcard suffixes (e.g. ".amazonaws.com")
}

// newDomainWhitelist creates a whitelist from a list of domain strings.
// Entries starting with "." are treated as subdomain wildcards.
func newDomainWhitelist(domains []string) *domainWhitelist {
	w := &domainWhitelist{exact: make(map[string]bool)}
	for _, d := range domains {
		d = strings.TrimSpace(strings.ToLower(d))
		if d == "" {
			continue
		}
		if strings.HasPrefix(d, ".") {
			w.suffixes = append(w.suffixes, d)
			// Also allow the bare domain (e.g. ".foo.com" allows "foo.com" too).
			w.exact[d[1:]] = true
		} else {
			w.exact[d] = true
		}
	}
	return w
}

// allowed checks if a hostname is in the whitelist.
func (w *domainWhitelist) allowed(host string) bool {
	host = strings.ToLower(host)
	if w.exact[host] {
		return true
	}
	for _, suffix := range w.suffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// count returns the total number of entries (exact + wildcard).
func (w *domainWhitelist) count() int {
	return len(w.exact) + len(w.suffixes)
}

// parseWhitelistFile reads a firewall.conf-style file and returns domain strings.
// Lines starting with # are comments; inline comments are stripped.
func parseWhitelistFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseWhitelistReader(bufio.NewScanner(f)), nil
}

func parseWhitelistReader(scanner *bufio.Scanner) []string {
	var domains []string
	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line != "" {
			domains = append(domains, line)
		}
	}
	return domains
}

// dedup removes duplicates from a string slice and returns sorted results.
func dedup(items []string) []string {
	seen := make(map[string]bool, len(items))
	var out []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}
