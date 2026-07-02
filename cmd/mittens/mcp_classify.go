package main

import (
	"strings"

	"github.com/SkrobyLabs/mittens/internal/mcpconfig"
)

type mcpShape string

const (
	mcpShapeRemote         mcpShape = "remote"
	mcpShapeStdioContainer mcpShape = "stdio-container"
	mcpShapeStdioHost      mcpShape = "stdio-host"
)

// mcpClassification describes a server's shape, the wizard's recommended mode,
// and any warnings (broad local capability) the wizard should surface.
type mcpClassification struct {
	Shape           mcpShape
	RecommendedMode string
	Warnings        []string
}

// broadCapabilityTokens are case-insensitive substrings that, if present in a
// server's command or args, indicate broad local capability (filesystem, shell,
// container control) that must never silently be proxied to the host.
var broadCapabilityTokens = []string{
	"server-filesystem", "shell", "exec", "terminal", "docker", "kubernetes",
}

// classifyMCPServer recommends a mode from a server's shape. It never
// recommends proxy for broad-capability servers.
func classifyMCPServer(s mcpconfig.Server) mcpClassification {
	c := mcpClassification{}
	if hasBroadLocalCapability(s) {
		c.Warnings = append(c.Warnings, "broad local capability (filesystem/shell/exec) — proxying grants host-level power; not recommended")
	}

	switch {
	case s.URL != "":
		c.Shape = mcpShapeRemote
		c.RecommendedMode = mcpModeDirect
	case isHostCommand(s.Command):
		c.Shape = mcpShapeStdioHost
		if len(s.Env) > 0 {
			c.RecommendedMode = mcpModeProxy
		} else {
			c.RecommendedMode = mcpModeMount
		}
	default:
		c.Shape = mcpShapeStdioContainer
		c.RecommendedMode = mcpModeDirect
	}
	// Broad-capability servers must never default to proxy.
	if c.RecommendedMode == mcpModeProxy && hasBroadLocalCapability(s) {
		c.RecommendedMode = mcpModeMount
	}
	return c
}

// isHostCommand reports whether the command resolves to a host-absolute path
// (rather than a container-resolvable binary like npx/uvx/node).
func isHostCommand(command string) bool {
	command = strings.TrimSpace(command)
	return command != "" && strings.ContainsRune(command, '/')
}

func hasBroadLocalCapability(s mcpconfig.Server) bool {
	haystack := strings.ToLower(s.Command + " " + strings.Join(s.Args, " "))
	for _, token := range broadCapabilityTokens {
		if strings.Contains(haystack, token) {
			return true
		}
	}
	return false
}
