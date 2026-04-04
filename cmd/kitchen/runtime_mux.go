package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

type runtimeMux struct {
	mu              sync.RWMutex
	runtimes        map[string]pool.RuntimeAPI
	providers       []string
	workerProviders map[string]string
}

func newRuntimeMux(clients map[string]pool.RuntimeAPI) *runtimeMux {
	mux := &runtimeMux{
		runtimes:        make(map[string]pool.RuntimeAPI, len(clients)),
		workerProviders: make(map[string]string),
	}
	for provider, client := range clients {
		key := canonicalKitchenProviderName(provider)
		if key == "" || client == nil {
			continue
		}
		mux.runtimes[key] = client
		mux.providers = append(mux.providers, key)
	}
	slices.Sort(mux.providers)
	return mux
}

func (m *runtimeMux) SpawnWorker(ctx context.Context, spec pool.WorkerSpec) (string, string, error) {
	provider, client, err := m.runtimeForSpec(spec)
	if err != nil {
		return "", "", err
	}
	containerName, containerID, err := client.SpawnWorker(ctx, spec)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(spec.ID) != "" {
		m.recordWorkerProvider(spec.ID, provider)
	}
	return containerName, containerID, nil
}

func (m *runtimeMux) KillWorker(ctx context.Context, workerID string) error {
	_, client := m.runtimeForWorker(workerID)
	if client != nil {
		if err := client.KillWorker(ctx, workerID); err != nil {
			return err
		}
		m.clearWorkerProvider(workerID)
		return nil
	}
	var firstErr error
	for _, candidate := range m.runtimeClients() {
		if err := candidate.KillWorker(ctx, workerID); err == nil {
			m.clearWorkerProvider(workerID)
			return nil
		} else if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return fmt.Errorf("kill worker %q: no runtime available", workerID)
}

func (m *runtimeMux) ListContainers(ctx context.Context, sessionID string) ([]pool.ContainerInfo, error) {
	var out []pool.ContainerInfo
	for _, provider := range m.providers {
		client := m.runtimes[provider]
		containers, err := client.ListContainers(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		for _, container := range containers {
			if strings.TrimSpace(container.WorkerID) != "" {
				m.recordWorkerProvider(container.WorkerID, provider)
			}
		}
		out = append(out, containers...)
	}
	return out, nil
}

func (m *runtimeMux) RecycleWorker(ctx context.Context, workerID string) error {
	_, client := m.runtimeForWorker(workerID)
	if client != nil {
		return client.RecycleWorker(ctx, workerID)
	}
	return m.tryEachRuntime(func(client pool.RuntimeAPI) error {
		return client.RecycleWorker(ctx, workerID)
	})
}

func (m *runtimeMux) GetWorkerActivity(ctx context.Context, workerID string) (*pool.WorkerActivity, error) {
	_, client := m.runtimeForWorker(workerID)
	if client != nil {
		return client.GetWorkerActivity(ctx, workerID)
	}
	for _, candidate := range m.runtimeClients() {
		activity, err := candidate.GetWorkerActivity(ctx, workerID)
		if err == nil && activity != nil {
			return activity, nil
		}
	}
	return nil, nil
}

func (m *runtimeMux) GetWorkerTranscript(ctx context.Context, workerID string) ([]pool.WorkerActivityRecord, error) {
	_, client := m.runtimeForWorker(workerID)
	if client != nil {
		return client.GetWorkerTranscript(ctx, workerID)
	}
	for _, candidate := range m.runtimeClients() {
		transcript, err := candidate.GetWorkerTranscript(ctx, workerID)
		if err == nil && len(transcript) > 0 {
			return transcript, nil
		}
	}
	return nil, nil
}

func (m *runtimeMux) SubscribeEvents(ctx context.Context) (<-chan pool.RuntimeEvent, error) {
	ch := make(chan pool.RuntimeEvent, 32)
	streams := make([]<-chan pool.RuntimeEvent, 0, len(m.providers))
	for _, provider := range m.providers {
		client := m.runtimes[provider]
		events, err := client.SubscribeEvents(ctx)
		if err != nil {
			return nil, err
		}
		streams = append(streams, events)
	}
	var wg sync.WaitGroup
	for i, events := range streams {
		provider := m.providers[i]
		wg.Add(1)
		go func(provider string, events <-chan pool.RuntimeEvent) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case event, ok := <-events:
					if !ok {
						return
					}
					if strings.TrimSpace(event.WorkerID) != "" {
						m.recordWorkerProvider(event.WorkerID, provider)
					}
					select {
					case ch <- event:
					case <-ctx.Done():
						return
					}
				}
			}
		}(provider, events)
	}
	go func() {
		wg.Wait()
		close(ch)
	}()
	return ch, nil
}

func (m *runtimeMux) SubmitAssignment(ctx context.Context, workerID string, assignment pool.Assignment) error {
	_, client := m.runtimeForWorker(workerID)
	if client != nil {
		return client.SubmitAssignment(ctx, workerID, assignment)
	}
	return m.tryEachRuntime(func(client pool.RuntimeAPI) error {
		return client.SubmitAssignment(ctx, workerID, assignment)
	})
}

func (m *runtimeMux) runtimeForSpec(spec pool.WorkerSpec) (string, pool.RuntimeAPI, error) {
	provider := canonicalKitchenProviderName(spec.Provider)
	if provider != "" {
		client := m.runtimes[provider]
		if client == nil {
			return "", nil, fmt.Errorf("spawn worker: no runtime configured for provider %q", spec.Provider)
		}
		return provider, client, nil
	}
	if len(m.providers) == 1 {
		provider := m.providers[0]
		return provider, m.runtimes[provider], nil
	}
	return "", nil, fmt.Errorf("spawn worker: provider is required when multiple runtimes are configured")
}

func (m *runtimeMux) runtimeForWorker(workerID string) (string, pool.RuntimeAPI) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return "", nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	provider := m.workerProviders[workerID]
	return provider, m.runtimes[provider]
}

func (m *runtimeMux) runtimeClients() []pool.RuntimeAPI {
	m.mu.RLock()
	defer m.mu.RUnlock()
	clients := make([]pool.RuntimeAPI, 0, len(m.providers))
	for _, provider := range m.providers {
		if client := m.runtimes[provider]; client != nil {
			clients = append(clients, client)
		}
	}
	return clients
}

func (m *runtimeMux) tryEachRuntime(fn func(pool.RuntimeAPI) error) error {
	var firstErr error
	for _, client := range m.runtimeClients() {
		if err := fn(client); err == nil {
			return nil
		} else if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	return fmt.Errorf("no runtime available")
}

func (m *runtimeMux) recordWorkerProvider(workerID, provider string) {
	workerID = strings.TrimSpace(workerID)
	provider = canonicalKitchenProviderName(provider)
	if workerID == "" || provider == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workerProviders[workerID] = provider
}

func (m *runtimeMux) clearWorkerProvider(workerID string) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workerProviders, workerID)
}
