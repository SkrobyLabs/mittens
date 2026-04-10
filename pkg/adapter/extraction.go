package adapter

import (
	"fmt"
	"regexp"
	"strings"
)

var structuredTagRegexes = map[string]*regexp.Regexp{
	"plan":         regexp.MustCompile(`(?is)<\s*plan\s*>(.*?)</\s*plan\s*>`),
	"council_turn": regexp.MustCompile(`(?is)<\s*council_turn\s*>(.*?)</\s*council_turn\s*>`),
	"handover":     regexp.MustCompile(`(?is)<\s*handover\s*>(.*?)</\s*handover\s*>`),
	"review":       regexp.MustCompile(`(?is)<\s*review\s*>(.*?)</\s*review\s*>`),
}

var innerTagRegexes = map[string]*regexp.Regexp{
	"summary":        regexp.MustCompile(`(?is)<\s*summary\s*>(.*?)</\s*summary\s*>`),
	"context":        regexp.MustCompile(`(?is)<\s*context\s*>(.*?)</\s*context\s*>`),
	"decisions":      regexp.MustCompile(`(?is)<\s*decisions\s*>(.*?)</\s*decisions\s*>`),
	"files_changed":  regexp.MustCompile(`(?is)<\s*files_changed\s*>(.*?)</\s*files_changed\s*>`),
	"open_questions": regexp.MustCompile(`(?is)<\s*open_questions\s*>(.*?)</\s*open_questions\s*>`),
	"verdict":        regexp.MustCompile(`(?is)<\s*verdict\s*>(.*?)</\s*verdict\s*>`),
	"feedback":       regexp.MustCompile(`(?is)<\s*feedback\s*>(.*?)</\s*feedback\s*>`),
	"severity":       regexp.MustCompile(`(?is)<\s*severity\s*>(.*?)</\s*severity\s*>`),
}

func extractTaggedBlock(output, tag string) (string, error) {
	return extractTaggedBlockWithMode(output, tag, false)
}

func extractTaggedBlockAllowEmpty(output, tag string) (string, error) {
	return extractTaggedBlockWithMode(output, tag, true)
}

func extractTaggedBlockWithMode(output, tag string, allowEmpty bool) (string, error) {
	regex, ok := structuredTagRegexes[tag]
	if !ok {
		return "", fmt.Errorf("unsupported tag %q", tag)
	}
	matches := regex.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return "", fmt.Errorf("%s block not found", blockLabel(tag))
	}
	body := strings.TrimSpace(matches[len(matches)-1][1])
	if body == "" && !allowEmpty {
		return "", fmt.Errorf("%s block is empty", blockLabel(tag))
	}
	return body, nil
}

func extractTaggedJSON(output, tag string) (string, error) {
	body, err := extractTaggedBlock(output, tag)
	if err != nil {
		return "", err
	}
	body = peelJSONFence(body)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("%s block is empty", blockLabel(tag))
	}
	return repairJSONBody(body), nil
}

func peelJSONFence(body string) string {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "```") || !strings.HasSuffix(body, "```") {
		return body
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 2 {
		return body
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if last != "```" {
		return body
	}
	switch strings.ToLower(first) {
	case "```", "```json":
		return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	default:
		return body
	}
}

func repairJSONBody(body string) string {
	if body == "" {
		return body
	}
	var b strings.Builder
	b.Grow(len(body))
	inString := false
	escaped := false
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if inString {
			b.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
			b.WriteByte(ch)
		case ',':
			j := i + 1
			for j < len(body) {
				switch body[j] {
				case ' ', '\t', '\n', '\r':
					j++
				default:
					goto next
				}
			}
		next:
			if j < len(body) && (body[j] == '}' || body[j] == ']') {
				continue
			}
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func extractTag(block, tag string) string {
	regex, ok := innerTagRegexes[tag]
	if !ok {
		return ""
	}
	matches := regex.FindAllStringSubmatch(block, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.TrimSpace(matches[len(matches)-1][1])
}

func blockLabel(tag string) string {
	return strings.ReplaceAll(tag, "_", " ")
}
