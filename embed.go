package main

import "embed"

//go:embed extensions/*/extension.yaml
var extensionYAMLs embed.FS
