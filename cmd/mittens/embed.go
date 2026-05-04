package main

import "embed"

//go:embed extensions/*/extension.yaml extensions/*/prompt.md
var extensionYAMLs embed.FS

//go:embed container/firewall.conf
var embeddedFirewallConf []byte

//go:embed container/firewall-dev.conf
var embeddedFirewallDevConf []byte
