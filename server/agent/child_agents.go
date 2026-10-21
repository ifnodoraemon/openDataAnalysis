package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/internal/idgen"
)

// maxRetainedChildren bounds how many delegated conversations a session keeps.
const maxRetainedChildren = 16

// ChildAgent is one delegated conversation.
//
// A delegated child is not a separate personality; it is a bounded execution
// unit. Keeping its conversation makes a follow-up instruction cheap: the child
// already holds what it inspected, so the parent does not have to restate it in
// a fresh child.
type ChildAgent struct {
	ID             string
	RoleName       string
	PolicyAppendix string
	AllowedTools   []string
	GoalID         string
	History        []ConversationItem
	LastResult     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ChildAgentRegistry stores delegated conversations for one session.
//
// It is process memory, like the job manager: after a restart the ids are
// unknown, and an unknown id is reported as such rather than silently starting
// a fresh conversation that looks like a continuation.
type ChildAgentRegistry struct {
	mu     sync.Mutex
	agents map[string]*ChildAgent
	order  []string
}

func NewChildAgentRegistry() *ChildAgentRegistry {
	return &ChildAgentRegistry{agents: make(map[string]*ChildAgent)}
}

// Create reserves a conversation for a new delegation.
func (r *ChildAgentRegistry) Create(roleName, policyAppendix string, allowedTools []string, goalID string) *ChildAgent {
	now := time.Now()
	child := &ChildAgent{
		ID:             idgen.Prefixed("sub_"),
		RoleName:       roleName,
		PolicyAppendix: policyAppendix,
		AllowedTools:   append([]string(nil), allowedTools...),
		GoalID:         goalID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if r == nil {
		return child
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[child.ID] = child
	r.order = append(r.order, child.ID)
	r.evictLocked()
	return child
}

// Get returns a copy of a stored conversation.
func (r *ChildAgentRegistry) Get(id string) (ChildAgent, error) {
	if r == nil {
		return ChildAgent{}, fmt.Errorf("delegated conversations are not available in this session")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	child, ok := r.agents[id]
	if !ok {
		return ChildAgent{}, fmt.Errorf("unknown subagent %q: its conversation is not available, because delegated conversations do not survive a process restart or session eviction", id)
	}
	return *child, nil
}

// Save writes back the conversation state after a turn of delegated work.
func (r *ChildAgentRegistry) Save(id string, history []ConversationItem, result string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	child, ok := r.agents[id]
	if !ok {
		return
	}
	child.History = append([]ConversationItem(nil), history...)
	child.LastResult = result
	child.UpdatedAt = time.Now()
}

// evictLocked drops the oldest conversations once the bound is exceeded.
func (r *ChildAgentRegistry) evictLocked() {
	for len(r.order) > maxRetainedChildren {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.agents, oldest)
	}
}
