package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const ProvenanceFile = ".mittens-source.json"

// ExtensionProvenance records the source at installation time. It is metadata,
// not a signature or a guarantee that installed files have not changed.
type ExtensionProvenance struct {
	Source   string `json:"source"`
	Revision string `json:"revision,omitempty"`
	Local    bool   `json:"local,omitempty"`
	Dirty    bool   `json:"dirty,omitempty"`
}

var extensionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ValidateExtensionName(name string) error {
	if !extensionNamePattern.MatchString(name) {
		return fmt.Errorf("invalid extension name %q: use letters, digits, '.', '_' or '-' and start with a letter or digit", name)
	}
	return nil
}

// SanitizeExtensionSource removes URL userinfo, queries, fragments, and terminal
// controls before a source is stored or displayed. Malformed URLs are hidden.
func SanitizeExtensionSource(source string) string {
	if strings.Contains(source, "://") {
		u, err := url.Parse(source)
		if err != nil || (u.Host == "" && (u.Scheme != "file" || u.Path == "")) {
			return "unknown URL"
		}
		u.User = nil
		u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
		u.ForceQuery = false
		source = u.String()
	} else if colon := strings.IndexByte(source, ':'); colon >= 0 && !strings.ContainsAny(source[:colon], `/\\`) {
		// Git also accepts SCP-style user@host:repository URLs.
		if at := strings.LastIndexByte(source[:colon], '@'); at >= 0 {
			source = source[at+1:]
		}
		if end := strings.IndexAny(source, "?#"); end >= 0 {
			source = source[:end]
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, source)
}

func (e *Extension) ProvenanceLabel() string {
	if e.Provenance == nil {
		return "source: unknown; revision: unknown (legacy or manual installation)"
	}
	p := e.Provenance
	source := SanitizeExtensionSource(p.Source)
	if source == "" {
		source = "unknown"
	}
	if p.Local {
		source += " (local)"
	}
	revision := p.Revision
	if !validGitRevision(revision) {
		revision = "unknown"
	}
	if p.Dirty {
		revision += " (local changes)"
	}
	return "source: " + source + "; revision: " + revision
}

func validGitRevision(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	for _, c := range revision {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
