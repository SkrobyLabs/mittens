package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type HealthEntry struct {
	CooldownUntil *time.Time `json:"cooldownUntil,omitempty"`
	AuthFailedAt  *time.Time `json:"authFailedAt,omitempty"`
}

type ProviderHealth struct {
	mu    sync.RWMutex
	state map[string]*HealthEntry
	path  string
}

func NewProviderHealth(path string) (*ProviderHealth, error) {
	ph := &ProviderHealth{
		state: make(map[string]*HealthEntry),
		path:  path,
	}
	if path == "" {
		return ph, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create provider health dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		if err := readJSONFile(path, &ph.state); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat provider health: %w", err)
	}
	ph.clearExpiredLocked(time.Now().UTC())
	if err := ph.persistLocked(); err != nil {
		return nil, err
	}
	return ph, nil
}

func (ph *ProviderHealth) Snapshot() map[string]HealthEntry {
	ph.mu.RLock()
	defer ph.mu.RUnlock()
	out := make(map[string]HealthEntry, len(ph.state))
	for key, entry := range ph.state {
		if entry == nil {
			continue
		}
		cp := *entry
		out[key] = cp
	}
	return out
}

func (ph *ProviderHealth) Get(key string) HealthEntry {
	ph.mu.RLock()
	defer ph.mu.RUnlock()
	entry := ph.state[key]
	if entry == nil {
		return HealthEntry{}
	}
	return *entry
}

func (ph *ProviderHealth) Available(key string, now time.Time) bool {
	ph.mu.RLock()
	defer ph.mu.RUnlock()
	entry := ph.state[key]
	if entry == nil {
		return true
	}
	if entry.AuthFailedAt != nil {
		return false
	}
	if entry.CooldownUntil != nil && entry.CooldownUntil.After(now) {
		return false
	}
	return true
}

func (ph *ProviderHealth) SetCooldown(key string, until time.Time) error {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	entry := ph.ensureEntryLocked(key)
	until = until.UTC()
	entry.CooldownUntil = &until
	return ph.persistLocked()
}

func (ph *ProviderHealth) MarkAuthFailure(key string, at time.Time) error {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	entry := ph.ensureEntryLocked(key)
	at = at.UTC()
	entry.AuthFailedAt = &at
	return ph.persistLocked()
}

func (ph *ProviderHealth) Reset(key string) error {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	delete(ph.state, key)
	return ph.persistLocked()
}

func (ph *ProviderHealth) ensureEntryLocked(key string) *HealthEntry {
	if ph.state[key] == nil {
		ph.state[key] = &HealthEntry{}
	}
	return ph.state[key]
}

func (ph *ProviderHealth) clearExpiredLocked(now time.Time) {
	for key, entry := range ph.state {
		if entry == nil {
			delete(ph.state, key)
			continue
		}
		if entry.CooldownUntil != nil && !entry.CooldownUntil.After(now) {
			entry.CooldownUntil = nil
		}
		if entry.CooldownUntil == nil && entry.AuthFailedAt == nil {
			delete(ph.state, key)
		}
	}
}

func (ph *ProviderHealth) persistLocked() error {
	if ph.path == "" {
		return nil
	}
	ph.clearExpiredLocked(time.Now().UTC())
	return writeJSONAtomic(ph.path, ph.state)
}
