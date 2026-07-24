package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
)

type sseClient struct {
	sessionID string
	send      chan agent.WSEvent
	done      chan struct{}
}

type SSEBroker struct {
	mu      sync.Mutex
	clients map[string]map[*sseClient]struct{}
}

var GlobalSSEBroker = &SSEBroker{
	clients: make(map[string]map[*sseClient]struct{}),
}

func (b *SSEBroker) Register(sessionID string) *sseClient {
	b.mu.Lock()
	defer b.mu.Unlock()

	client := &sseClient{
		sessionID: sessionID,
		send:      make(chan agent.WSEvent, 100),
		done:      make(chan struct{}),
	}

	if b.clients[sessionID] == nil {
		b.clients[sessionID] = make(map[*sseClient]struct{})
	}
	b.clients[sessionID][client] = struct{}{}
	return client
}

func (b *SSEBroker) Unregister(client *sseClient) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if m, ok := b.clients[client.sessionID]; ok {
		if _, exists := m[client]; exists {
			delete(m, client)
			close(client.done)
		}
		if len(m) == 0 {
			delete(b.clients, client.sessionID)
		}
	}
}

func (b *SSEBroker) Broadcast(sessionID string, evt agent.WSEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if m, ok := b.clients[sessionID]; ok {
		for client := range m {
			select {
			case client.send <- evt:
			default:
				// Buffer full, skip
			}
		}
	}
}

func SSEHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusBadRequest)
		return
	}

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	client := GlobalSSEBroker.Register(sessionID)
	defer GlobalSSEBroker.Unregister(client)

	connEvt, _ := json.Marshal(map[string]any{
		"type": "connected",
		"data": map[string]string{"session_id": sessionID},
	})
	fmt.Fprintf(w, "data: %s\n\n", connEvt)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-client.done:
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		case evt, ok := <-client.send:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
