package transport

import (
	"sort"
	"sync"

	"pi-remote/internal/protocol"
)

type Hub struct {
	mu      sync.RWMutex
	workers map[string]*Peer
}

func NewHub() *Hub {
	return &Hub{workers: make(map[string]*Peer)}
}

func (h *Hub) Register(peer *Peer) {
	h.mu.Lock()
	previous := h.workers[peer.Hello.WorkerID]
	h.workers[peer.Hello.WorkerID] = peer
	h.mu.Unlock()
	if previous != nil && previous != peer {
		previous.Close()
	}
}

func (h *Hub) Remove(id string, peer *Peer) {
	h.mu.Lock()
	if h.workers[id] == peer {
		delete(h.workers, id)
	}
	h.mu.Unlock()
}

func (h *Hub) Get(id string) (*Peer, bool) {
	h.mu.RLock()
	peer, ok := h.workers[id]
	h.mu.RUnlock()
	return peer, ok
}

func (h *Hub) List() []protocol.Hello {
	h.mu.RLock()
	workers := make([]protocol.Hello, 0, len(h.workers))
	for _, peer := range h.workers {
		workers = append(workers, peer.Hello)
	}
	h.mu.RUnlock()
	sort.Slice(workers, func(i, j int) bool { return workers[i].WorkerID < workers[j].WorkerID })
	return workers
}

func (h *Hub) Close() {
	h.mu.RLock()
	peers := make([]*Peer, 0, len(h.workers))
	for _, peer := range h.workers {
		peers = append(peers, peer)
	}
	h.mu.RUnlock()
	for _, peer := range peers {
		peer.Close()
	}
}
