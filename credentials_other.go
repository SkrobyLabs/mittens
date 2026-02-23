//go:build !darwin

package main

// newKeychainStore returns nil on non-macOS platforms where no system keychain
// integration is available.
func newKeychainStore() CredentialStore {
	return nil
}
