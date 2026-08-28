package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	maxMemoryEntries   = 50
	maxMemoryKeyLength = 200
	maxStatementLength = 2000
	maxGoals           = 30
	maxGoalDescLen     = 500
)

// WorkingMemory stores typed statements selected by the model during a run.
type WorkingMemory struct {
	Entries map[string]MemoryEntry `json:"entries"`
	mu      sync.RWMutex
}

type MemoryEntry struct {
	Statement       string    `json:"statement"`
	Status          string    `json:"status"`
	SourceResultIDs []string  `json:"source_result_ids,omitempty"`
	Confidence      *float64  `json:"confidence,omitempty"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
}

func NewWorkingMemory() *WorkingMemory {
	return &WorkingMemory{
		Entries: make(map[string]MemoryEntry),
	}
}

func validateMemoryEntry(key string, entry MemoryEntry) error {
	if key == "" || key != strings.TrimSpace(key) || len(key) > maxMemoryKeyLength {
		return fmt.Errorf("memory key must be a non-empty exact value of at most %d bytes", maxMemoryKeyLength)
	}
	if strings.TrimSpace(entry.Statement) == "" || len(entry.Statement) > maxStatementLength {
		return fmt.Errorf("memory statement must contain text and be at most %d bytes", maxStatementLength)
	}
	if entry.Status != "observed" && entry.Status != "inferred" && entry.Status != "assumed" {
		return fmt.Errorf("memory status must be observed, inferred, or assumed")
	}
	if entry.Status == "observed" && len(entry.SourceResultIDs) == 0 {
		return fmt.Errorf("observed memory entries require source result IDs")
	}
	if entry.Confidence != nil && (*entry.Confidence < 0 || *entry.Confidence > 1) {
		return fmt.Errorf("memory confidence must be between 0 and 1")
	}
	seenResults := make(map[string]struct{}, len(entry.SourceResultIDs))
	for _, resultID := range entry.SourceResultIDs {
		if resultID == "" || resultID != strings.TrimSpace(resultID) {
			return fmt.Errorf("memory source result IDs must be non-empty exact values")
		}
		if _, exists := seenResults[resultID]; exists {
			return fmt.Errorf("memory source result ID %q is duplicated", resultID)
		}
		seenResults[resultID] = struct{}{}
	}
	return nil
}

func (m *WorkingMemory) SaveEntry(key string, entry MemoryEntry) (bool, error) {
	if err := validateMemoryEntry(key, entry); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Entries) >= maxMemoryEntries {
		if _, exists := m.Entries[key]; !exists {
			return false, nil
		}
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if m.Entries == nil {
		m.Entries = make(map[string]MemoryEntry)
	}
	m.Entries[key] = entry
	return true, nil
}

// RemoveEntry removes one memory entry.
func (m *WorkingMemory) RemoveEntry(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Entries, key)
}

func (m *WorkingMemory) EntrySnapshot() map[string]MemoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]MemoryEntry, len(m.Entries))
	for key, value := range m.Entries {
		value.SourceResultIDs = append([]string(nil), value.SourceResultIDs...)
		result[key] = value
	}
	return result
}

// Snapshot returns display values derived from typed memory entries.
func (m *WorkingMemory) Snapshot() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make(map[string]string, len(m.Entries))
	for k, entry := range m.Entries {
		res[k] = entry.Statement
	}
	return res
}

// Reset 清空所有工作记忆。
func (m *WorkingMemory) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Entries = make(map[string]MemoryEntry)
}

// ReplaceEntries restores typed entries from persisted state events.
func (m *WorkingMemory) ReplaceEntries(entries map[string]MemoryEntry) error {
	if len(entries) > maxMemoryEntries {
		return fmt.Errorf("persisted working memory exceeds the %d entry limit", maxMemoryEntries)
	}
	replacement := make(map[string]MemoryEntry, len(entries))
	for k, entry := range entries {
		if err := validateMemoryEntry(k, entry); err != nil {
			return fmt.Errorf("invalid persisted memory entry %q: %w", k, err)
		}
		if entry.CreatedAt.IsZero() {
			return fmt.Errorf("invalid persisted memory entry %q: created_at is required", k)
		}
		entry.SourceResultIDs = append([]string(nil), entry.SourceResultIDs...)
		replacement[k] = entry
	}
	m.mu.Lock()
	m.Entries = replacement
	m.mu.Unlock()
	return nil
}

type SubgoalStatus string

const (
	StatusPending  SubgoalStatus = "pending"
	StatusRunning  SubgoalStatus = "running"
	StatusComplete SubgoalStatus = "complete"
	StatusRejected SubgoalStatus = "rejected"
)

type Subgoal struct {
	ID           string        `json:"id"`
	ParentGoalID string        `json:"parentGoalId,omitempty"`
	Description  string        `json:"description"`
	Status       SubgoalStatus `json:"status"`
	Blocking     bool          `json:"blocking,omitempty"`
	Result       string        `json:"result,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
}

// SubgoalManager 维护 Agent 当前计划的待解决问题树
type SubgoalManager struct {
	Goals []Subgoal `json:"goals"`
	mu    sync.RWMutex
}

type finalizeSnapshot struct {
	roots    []Subgoal
	children map[string][]Subgoal
}

func NewSubgoalManager() *SubgoalManager {
	return &SubgoalManager{
		Goals: make([]Subgoal, 0),
	}
}

func (s *SubgoalManager) AddGoalWithBlocking(description, parentGoalID string, blocking bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.Goals) >= maxGoals {
		return "", fmt.Errorf("subgoal limit (%d) reached", maxGoals)
	}
	if strings.TrimSpace(description) == "" || len(description) > maxGoalDescLen {
		return "", fmt.Errorf("goal description must contain text and be at most %d bytes", maxGoalDescLen)
	}
	if parentGoalID != strings.TrimSpace(parentGoalID) {
		return "", fmt.Errorf("parent goal ID must be an exact value")
	}
	if parentGoalID != "" {
		parentExists := false
		for _, goal := range s.Goals {
			if goal.ID == parentGoalID {
				parentExists = true
				break
			}
		}
		if !parentExists {
			return "", fmt.Errorf("parent goal %s not found", parentGoalID)
		}
	}

	id := "goal_" + uuid.New().String()[:8]
	s.Goals = append(s.Goals, Subgoal{
		ID:           id,
		ParentGoalID: parentGoalID,
		Description:  description,
		Status:       StatusPending,
		Blocking:     blocking,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	return id, nil
}

// UpdateGoalStatus 更新任务状态（完成、拒绝等）
func (s *SubgoalManager) UpdateGoalStatus(id string, status SubgoalStatus, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.Goals {
		if s.Goals[i].ID == id {
			s.Goals[i].Status = status
			if result != "" {
				s.Goals[i].Result = result
			}
			s.Goals[i].UpdatedAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("subgoal with ID %s not found", id)
}

func (s *SubgoalManager) HasGoal(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, goal := range s.Goals {
		if goal.ID == id {
			return true
		}
	}
	return false
}

func isTerminalSubgoalStatus(status SubgoalStatus) bool {
	return status == StatusComplete || status == StatusRejected
}

func (s *SubgoalManager) snapshotLocked() finalizeSnapshot {
	byID := make(map[string]Subgoal, len(s.Goals))
	for _, g := range s.Goals {
		byID[g.ID] = g
	}

	children := make(map[string][]Subgoal, len(s.Goals))
	roots := make([]Subgoal, 0)
	for _, g := range s.Goals {
		parentID := g.ParentGoalID
		if parentID == "" {
			roots = append(roots, g)
			continue
		}
		children[parentID] = append(children[parentID], g)
	}

	return finalizeSnapshot{
		roots:    roots,
		children: children,
	}
}

func (s *SubgoalManager) collectActiveBranchLines(snapshot finalizeSnapshot) []string {
	if len(snapshot.roots) == 0 {
		return nil
	}

	branches := make([]string, 0)
	var dfs func(goal Subgoal, path []string)
	dfs = func(goal Subgoal, path []string) {
		if isTerminalSubgoalStatus(goal.Status) {
			return
		}

		step := fmt.Sprintf("%s[%s]", goal.Description, goal.Status)
		path = append(path, step)

		hasNonTerminalChild := false
		for _, child := range snapshot.children[goal.ID] {
			if isTerminalSubgoalStatus(child.Status) {
				continue
			}
			hasNonTerminalChild = true
			dfs(child, path)
		}
		if !hasNonTerminalChild {
			branches = append(branches, strings.Join(path, " -> "))
		}
	}

	for _, root := range snapshot.roots {
		if isTerminalSubgoalStatus(root.Status) || !root.Blocking {
			continue
		}
		dfs(root, nil)
	}
	return branches
}

// CanFinalize 检查当前是否允许结束。
// 判定只基于显式 blocking 的根目标是否闭环；scratchpad 目标不会阻塞交付。
// 已闭环根目标下面遗留的历史子步骤不会继续阻塞结束。
func (s *SubgoalManager) CanFinalize() (bool, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.Goals) == 0 {
		return true, nil
	}

	snapshot := s.snapshotLocked()
	blockers := s.collectActiveBranchLines(snapshot)
	return len(blockers) == 0, blockers
}

// ListAll 导出当前所有的目标（返回副本，避免外部由于并发修改切片导致 panic）
func (s *SubgoalManager) ListAll() []Subgoal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]Subgoal, len(s.Goals))
	copy(res, s.Goals)
	return res
}

// Reset 清空全部子目标。
func (s *SubgoalManager) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Goals = make([]Subgoal, 0)
}

// ReplaceAll validates and atomically restores a persisted goal tree.
func (s *SubgoalManager) ReplaceAll(goals []Subgoal) error {
	if len(goals) > maxGoals {
		return fmt.Errorf("persisted goal tree exceeds the %d goal limit", maxGoals)
	}
	byID := make(map[string]Subgoal, len(goals))
	for _, goal := range goals {
		if goal.ID == "" || goal.ID != strings.TrimSpace(goal.ID) {
			return fmt.Errorf("persisted goal ID must be a non-empty exact value")
		}
		if _, exists := byID[goal.ID]; exists {
			return fmt.Errorf("persisted goal ID %q is duplicated", goal.ID)
		}
		if strings.TrimSpace(goal.Description) == "" || len(goal.Description) > maxGoalDescLen {
			return fmt.Errorf("persisted goal %s has an invalid description", goal.ID)
		}
		switch goal.Status {
		case StatusPending, StatusRunning, StatusComplete, StatusRejected:
		default:
			return fmt.Errorf("persisted goal %s has invalid status %q", goal.ID, goal.Status)
		}
		if goal.ParentGoalID != strings.TrimSpace(goal.ParentGoalID) {
			return fmt.Errorf("persisted goal %s has a non-exact parent ID", goal.ID)
		}
		if goal.CreatedAt.IsZero() || goal.UpdatedAt.IsZero() {
			return fmt.Errorf("persisted goal %s is missing timestamps", goal.ID)
		}
		byID[goal.ID] = goal
	}
	for _, goal := range goals {
		if goal.ParentGoalID != "" {
			if _, exists := byID[goal.ParentGoalID]; !exists {
				return fmt.Errorf("persisted goal %s references missing parent %s", goal.ID, goal.ParentGoalID)
			}
		}
		seen := map[string]struct{}{goal.ID: {}}
		parentID := goal.ParentGoalID
		for parentID != "" {
			if _, exists := seen[parentID]; exists {
				return fmt.Errorf("persisted goal tree contains a cycle at %s", parentID)
			}
			seen[parentID] = struct{}{}
			parentID = byID[parentID].ParentGoalID
		}
	}
	replacement := append([]Subgoal(nil), goals...)
	s.mu.Lock()
	s.Goals = replacement
	s.mu.Unlock()
	return nil
}
