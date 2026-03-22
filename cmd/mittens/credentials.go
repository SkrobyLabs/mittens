package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SkrobyLabs/mittens/internal/credutil"
)

// CredentialStore abstracts a credential source (file, keychain, etc.).
type CredentialStore interface {
	// Extract returns the raw JSON credentials, or empty string if unavailable.
	Extract() (string, error)
	// Persist writes credentials back to the store.
	Persist(jsonData string) error
	// Label returns a human-readable source name.
	Label() string
}

// CredentialManager coordinates credential extraction, staging, and persistence
// across multiple credential stores.
type CredentialManager struct {
	stores  []CredentialStore
	tmpFile string
}

// NewCredentialManager creates a manager with the platform credential stores.
// On macOS the keychain store is included; on other platforms only the file store
// is used. Paths and service names are derived from the given provider.
func NewCredentialManager(provider *Provider) *CredentialManager {
	var stores []CredentialStore

	// Platform-specific store (keychain on darwin, nil on others).
	if ks := newKeychainStore(provider.KeychainService); ks != nil {
		stores = append(stores, ks)
	}

	// File-based store only when a credential file is configured.
	if provider.CredentialFile != "" {
		home := os.Getenv("HOME")
		stores = append(stores, &FileCredentialStore{
			path: provider.HostCredentialPath(home),
		})
	}

	return &CredentialManager{stores: stores}
}

// Stores returns the underlying credential stores for use by the broker.
func (m *CredentialManager) Stores() []CredentialStore {
	return m.stores
}

// Setup extracts credentials from the freshest source and writes them to a
// temporary file that can be mounted into the container.
func (m *CredentialManager) Setup() error {
	var sources []credentialSource
	for _, s := range m.stores {
		data, err := s.Extract()
		if err != nil || data == "" {
			continue
		}
		sources = append(sources, credentialSource{json: data, label: s.Label()})
	}

	if len(sources) == 0 {
		return nil // no credentials available; caller decides how to proceed
	}

	best, label := freshestCredential(sources)

	tmp, err := os.CreateTemp("", "mittens-cred.*.json")
	if err != nil {
		return fmt.Errorf("creating credential temp file: %w", err)
	}

	if err := os.Chmod(tmp.Name(), 0600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("setting credential temp file permissions: %w", err)
	}

	if _, err := tmp.WriteString(best); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("writing credential temp file: %w", err)
	}
	tmp.Close()

	m.tmpFile = tmp.Name()
	logInfo("OAuth credentials loaded from %s", label)
	return nil
}

// TmpFile returns the path to the temporary credential file, or empty string
// if no credentials were staged.
func (m *CredentialManager) TmpFile() string {
	return m.tmpFile
}

// PersistAll writes the given JSON credentials to all known stores.
func (m *CredentialManager) PersistAll(jsonData string) {
	for _, s := range m.stores {
		if err := s.Persist(jsonData); err != nil {
			logWarn("Failed to persist credentials to %s: %v", s.Label(), err)
		}
	}
}

// Cleanup removes the temporary credential file.
func (m *CredentialManager) Cleanup() {
	if m.tmpFile != "" {
		os.Remove(m.tmpFile)
		m.tmpFile = ""
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// expiresAt extracts the highest expiry timestamp from credential JSON.
// Delegates to the shared credutil package.
func expiresAt(jsonData string) int64 {
	return credutil.ExpiresAtString(jsonData)
}

// credentialSource pairs raw JSON with a human-readable label.
type credentialSource struct {
	json  string
	label string
}

// freshestCredential picks the source with the highest expiresAt timestamp.
func freshestCredential(sources []credentialSource) (string, string) {
	if len(sources) == 0 {
		return "", ""
	}

	bestJSON := sources[0].json
	bestLabel := sources[0].label
	bestExp := expiresAt(sources[0].json)

	for _, s := range sources[1:] {
		exp := expiresAt(s.json)
		if exp > bestExp {
			bestJSON = s.json
			bestLabel = s.label
			bestExp = exp
		}
	}
	return bestJSON, bestLabel
}

// ---------------------------------------------------------------------------
// FileCredentialStore
// ---------------------------------------------------------------------------

// FileCredentialStore reads and writes credentials to a JSON file on disk.
type FileCredentialStore struct {
	path string
}

func (f *FileCredentialStore) Extract() (string, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func (f *FileCredentialStore) Persist(jsonData string) error {
	dir := filepath.Dir(f.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating credential directory: %w", err)
	}
	return os.WriteFile(f.path, []byte(jsonData), 0600)
}

func (f *FileCredentialStore) Label() string {
	return f.path
}
