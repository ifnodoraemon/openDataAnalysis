package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
)

type sseMessage struct {
	id  string
	evt agent.RuntimeEvent
}

type sseClient struct {
	sessionID string
	send      chan sseMessage
	done      chan struct{}
	dropped   atomic.Uint64
	overflow  atomic.Bool
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
		send:      make(chan sseMessage, 100),
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

func (b *SSEBroker) CloseSession(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for client := range b.clients[sessionID] {
		close(client.done)
	}
	delete(b.clients, sessionID)
}

func (b *SSEBroker) Broadcast(sessionID string, evt agent.RuntimeEvent, messageID string) {
	b.mu.Lock()
	clients := make([]*sseClient, 0, len(b.clients[sessionID]))
	for client := range b.clients[sessionID] {
		clients = append(clients, client)
	}
	b.mu.Unlock()

	for _, client := range clients {
		select {
		case client.send <- sseMessage{id: messageID, evt: evt}:
		case <-client.done:
		default:
			client.dropped.Add(1)
			client.overflow.Store(true)
		}
	}
}

func SSEHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "当前连接不支持事件流", http.StatusBadRequest)
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "缺少会话 ID", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(sessionID) != sessionID {
		http.Error(w, "会话 ID 必须保持原值", http.StatusBadRequest)
		return
	}
	identity, authenticated := auth.FromContext(r.Context())
	if !authenticated {
		http.Error(w, "需要登录", http.StatusUnauthorized)
		return
	}
	sess, err := sessionRepo.GetByID(r.Context(), sessionID)
	if writeRepoLookupError(w, err, "会话不存在") {
		return
	}
	if sess.WorkspaceID != identity.WorkspaceID || sess.UserID != identity.UserID {
		http.Error(w, "无权访问该会话", http.StatusForbidden)
		return
	}

	lastEventID := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if lastEventID == "" {
		lastEventID = strings.TrimSpace(r.URL.Query().Get("last_event_id"))
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	client := GlobalSSEBroker.Register(sessionID)
	metrics.ActiveEventStreamConnections.Inc()
	defer func() {
		GlobalSSEBroker.Unregister(client)
		metrics.ActiveEventStreamConnections.Dec()
	}()

	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("SSE write deadline control failed session_id=%s err=%v", sessionID, err)
	}

	connEvt, err := json.Marshal(map[string]any{
		"type": "connected",
		"data": map[string]string{"sessionId": sessionID},
	})
	if err != nil {
		http.Error(w, "编码事件流消息失败", http.StatusInternalServerError)
		return
	}
	if err := writeSSEEvent(w, "", connEvt); err != nil {
		return
	}
	flusher.Flush()

	replayedIDs := replaySessionMessages(w, flusher, r, sessionID, lastEventID)

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		if client.overflow.Load() {
			if err := writeSSEOverflow(w, client.dropped.Load()); err != nil {
				log.Printf("SSE overflow event write failed session_id=%s err=%v", sessionID, err)
			}
			flusher.Flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-client.done:
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case msg := <-client.send:
			delivered, err := deliverSSEMessage(w, msg, replayedIDs)
			if err != nil {
				log.Printf("SSE event write failed session_id=%s type=%s err=%v", sessionID, msg.evt.Type, err)
				return
			}
			if delivered {
				flusher.Flush()
			}
		}
	}
}

func deliverSSEMessage(w io.Writer, msg sseMessage, replayed map[string]struct{}) (bool, error) {
	if msg.id != "" {
		if _, dup := replayed[msg.id]; dup {
			return false, nil
		}
	}
	data, err := json.Marshal(msg.evt)
	if err != nil {
		return false, err
	}
	if err := writeSSEEvent(w, msg.id, data); err != nil {
		return false, err
	}
	return true, nil
}

func replaySessionMessages(w io.Writer, flusher http.Flusher, r *http.Request, sessionID, lastEventID string) map[string]struct{} {
	replayed := make(map[string]struct{})
	if lastEventID == "" {
		return replayed
	}
	messages, err := collectSessionMessages(r.Context(), sessionID)
	if err != nil {
		log.Printf("SSE replay load failed session_id=%s cursor=%s err=%v", sessionID, clipLogText(lastEventID, 64), err)
		return replayed
	}
	for _, msg := range sessionReplayMessages(messages, lastEventID) {
		evt, err := runMessageToRuntimeEvent(msg)
		if err != nil {
			log.Printf("SSE replay event decode failed session_id=%s message_id=%s type=%s err=%v", sessionID, msg.ID, msg.Type, err)
			continue
		}
		data, err := json.Marshal(evt)
		if err != nil {
			log.Printf("SSE replay event encoding failed session_id=%s message_id=%s type=%s err=%v", sessionID, msg.ID, msg.Type, err)
			continue
		}
		if err := writeSSEEvent(w, msg.ID, data); err != nil {
			return replayed
		}
		replayed[msg.ID] = struct{}{}
	}
	flusher.Flush()
	return replayed
}

func sessionReplayMessages(messages []domain.RunMessage, cursor string) []domain.RunMessage {
	if cursor == "" {
		return nil
	}
	for i, msg := range messages {
		if msg.ID == cursor {
			return messages[i+1:]
		}
	}
	return messages
}

func runMessageToRuntimeEvent(msg domain.RunMessage) (agent.RuntimeEvent, error) {
	evt := agent.RuntimeEvent{
		Type:      msg.Type,
		SessionID: msg.SessionID,
		RunID:     msg.RunID,
	}
	switch msg.Type {
	case "user":
		evt.Data = agent.UserMessage{Content: msg.Content}
	case string(agent.EventAssistantStatus):
		evt.Data = agent.AssistantStatusData{Content: msg.Content}
	case string(agent.EventError), string(agent.EventRunCancelled):
		evt.Data = agent.ErrorData{Message: msg.Content}
	case string(agent.EventRunCompleted):
		evt.Data = agent.CompleteData{Summary: msg.Content}
	case string(agent.EventToolCall):
		data := agent.ToolCallData{Name: msg.Name, Arguments: json.RawMessage(msg.Content)}
		if msg.ToolCallID != nil {
			data.ID = *msg.ToolCallID
		}
		evt.Data = data
	case string(agent.EventToolResult):
		data := agent.ToolResultData{Name: msg.Name, Result: msg.Content}
		if msg.ToolCallID != nil {
			data.ID = *msg.ToolCallID
		}
		if msg.Duration != nil {
			data.Duration = *msg.Duration
		}
		if msg.Success != nil {
			data.Success = *msg.Success
		}
		evt.Data = data
	case string(agent.EventUserRequestInput):
		var data agent.AskUserData
		if err := json.Unmarshal([]byte(msg.Content), &data); err != nil {
			return agent.RuntimeEvent{}, err
		}
		evt.Data = data
	case string(agent.EventReportUpdate), string(agent.EventReportFinal):
		var data agent.ReportUpdateData
		if err := json.Unmarshal([]byte(msg.Content), &data); err != nil {
			return agent.RuntimeEvent{}, err
		}
		evt.Data = data
	default:
		evt.Data = json.RawMessage(msg.Content)
	}
	return evt, nil
}

func writeSSEEvent(w io.Writer, id string, data []byte) error {
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func writeSSEOverflow(w io.Writer, dropped uint64) error {
	payload, err := json.Marshal(map[string]any{"type": "overflow", "dropped": dropped})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: overflow\ndata: %s\n\n", payload)
	return err
}
