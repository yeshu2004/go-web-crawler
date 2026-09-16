package router

import (
	"context"
	"sync"
)

type CrawlerManager struct {
	mu      sync.Mutex
	running map[string]context.CancelFunc
}

func NewCrawlerManager() *CrawlerManager {
	return &CrawlerManager{
		running: make(map[string]context.CancelFunc),
	}
}

func (m *CrawlerManager) Add(id string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.running[id] = cancel
}

func (m *CrawlerManager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.running, id)
}

func (m *CrawlerManager) Stop(id string) bool {
	m.mu.Lock()
	cancel, ok := m.running[id]
	m.mu.Unlock()

	if !ok {
		return false
	}

	cancel()
	return true
}