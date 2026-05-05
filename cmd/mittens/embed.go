package main

import "embed"

//go:embed container/* extensions/*/*.yaml extensions/*/*.sh extensions/*/*.md
var runtimeAssets embed.FS

//go:embed extensions/*/extension.yaml extensions/*/prompt.md
var extensionYAMLs embed.FS

//go:embed container/firewall.conf
var embeddedFirewallConf []byte

//go:embed container/firewall-dev.conf
var embeddedFirewallDevConf []byte
