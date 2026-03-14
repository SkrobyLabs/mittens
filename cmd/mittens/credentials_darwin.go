//go:build darwin

package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// KeychainStore reads and writes credentials from the macOS Keychain.
type KeychainStore struct {
	service string
}

func newKeychainStore(service string) CredentialStore {
	if service == "" {
		return nil
	}
	return &KeychainStore{service: service}
}

func (k *KeychainStore) Extract() (string, error) {
	// -w prints only the password (the JSON blob).
	out, err := exec.Command("security", "find-generic-password",
		"-s", k.service, "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (k *KeychainStore) Persist(jsonData string) error {
	acct, err := k.accountName()
	if err != nil || acct == "" {
		// No existing entry; nothing to update.
		return nil
	}

	cmd := exec.Command("security", "add-generic-password", "-U",
		"-s", k.service, "-a", acct, "-w", jsonData)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain update: %w: %s", err, out)
	}
	return nil
}

func (k *KeychainStore) Label() string {
	return "keychain"
}

// accountName retrieves the account name from the existing keychain entry by
// parsing the "acct"<blob>="..." field from `security find-generic-password`.
var acctRegexp = regexp.MustCompile(`"acct"<blob>="([^"]*)"`)

func (k *KeychainStore) accountName() (string, error) {
	out, err := exec.Command("security", "find-generic-password",
		"-s", k.service).Output()
	if err != nil {
		return "", err
	}
	matches := acctRegexp.FindSubmatch(out)
	if len(matches) < 2 {
		return "", nil
	}
	return string(matches[1]), nil
}
