package agent

import (
	"encoding/json"
	"testing"
)

func TestWorkingMemory(t *testing.T) {
	mem := NewWorkingMemory()

	mem.SaveEntry("key1", MemoryEntry{Statement: "fact1", Status: "inferred", CreatedBy: "test"})
	mem.SaveEntry("key2", MemoryEntry{Statement: "fact2", Status: "observed", SourceResultIDs: []string{"res_test"}, CreatedBy: "test"})

	mem.RemoveEntry("key1")
	if _, exists := mem.Snapshot()["key1"]; exists {
		t.Errorf("expected snapshot to not contain key1")
	}

	mem.Reset()
	if len(mem.Snapshot()) != 0 {
		t.Errorf("expected snapshot to be empty after reset")
	}
}

func TestSubgoalManager(t *testing.T) {
	sm := NewSubgoalManager()

	canFinalize, blockers := sm.CanFinalize()
	if !canFinalize || len(blockers) != 0 {
		t.Errorf("expected empty subgoals to allow finalize")
	}

	// Test AddGoal
	id1, _ := sm.AddGoalWithBlocking("goal 1", "", false)
	id2, _ := sm.AddGoalWithBlocking("goal 2", id1, false)

	canFinalize, blockers = sm.CanFinalize()
	if !canFinalize || len(blockers) != 0 {
		t.Errorf("expected non-blocking scratchpad goal to allow finalize, blockers=%v", blockers)
	}

	blockingRootID, _ := sm.AddGoalWithBlocking("blocking goal", "", true)
	_, _ = sm.AddGoalWithBlocking("blocking child", blockingRootID, false)

	canFinalize, blockers = sm.CanFinalize()
	if canFinalize || len(blockers) != 1 {
		t.Errorf("expected pending blocking root goal to block finalize, blockers=%v", blockers)
	}

	err := sm.UpdateGoalStatus(blockingRootID, StatusComplete, "done")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Test UpdateGoalStatus
	err = sm.UpdateGoalStatus(id1, StatusComplete, "done")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	canFinalize, blockers = sm.CanFinalize()
	if !canFinalize || len(blockers) != 0 {
		t.Errorf("expected completed root to allow finalize, blockers=%v", blockers)
	}

	err = sm.UpdateGoalStatus(id2, StatusRejected, "rejected")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	canFinalize, blockers = sm.CanFinalize()
	if !canFinalize || len(blockers) != 0 {
		t.Errorf("expected finalize to be allowed as root/branch are terminal, blockers=%v", blockers)
	}

	if sm.Goals[1].ParentGoalID != id1 {
		t.Errorf("expected nested goal parent id to be preserved")
	}
}

func TestSubgoalManagerAllowsFinalizeWhenClosedRootHasStaleChildren(t *testing.T) {
	sm := NewSubgoalManager()
	rootID, _ := sm.AddGoalWithBlocking("root goal", "", true)
	childID, _ := sm.AddGoalWithBlocking("stale child", rootID, false)

	if err := sm.UpdateGoalStatus(rootID, StatusComplete, "done"); err != nil {
		t.Fatalf("update root status: %v", err)
	}

	canFinalize, blockers := sm.CanFinalize()
	if !canFinalize {
		t.Fatalf("expected completed root to allow finalize even with stale child, blockers=%v", blockers)
	}

	goals := sm.ListAll()
	foundStaleChild := false
	for _, goal := range goals {
		if goal.ID == childID && goal.Description == "stale child" && goal.Status == StatusPending {
			foundStaleChild = true
			break
		}
	}
	if !foundStaleChild {
		t.Fatalf("expected stale child to remain in goal facts: %#v", goals)
	}

	if err := sm.UpdateGoalStatus(childID, StatusRejected, "obsolete"); err != nil {
		t.Fatalf("update child status: %v", err)
	}
}

func TestSaveMemoryTool(t *testing.T) {
	mem := NewWorkingMemory()
	tool := &SaveMemoryTool{Memory: mem, EmitFunc: func(RuntimeEvent) {}}

	args := []byte(`{"key":"test_key","statement":"test_fact","status":"inferred"}`)
	res, err := tool.Execute(args)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		t.Fatalf("expected save memory json payload: %v", err)
	}
	if payload["tool"] != "memory_save_entry" || payload["memory_key"] != "test_key" {
		t.Fatalf("unexpected save memory payload: %#v", payload)
	}
	if payload["entry_count"] != float64(1) {
		t.Fatalf("expected entry_count=1, got %#v", payload["entry_count"])
	}
	if payload["overwrote_existing"] != false {
		t.Fatalf("expected overwrote_existing=false, got %#v", payload["overwrote_existing"])
	}
	if payload["affects_report_delivery"] != false {
		t.Fatalf("expected affects_report_delivery=false, got %#v", payload["affects_report_delivery"])
	}
	if mem.EntrySnapshot()["test_key"].Statement != "test_fact" {
		t.Errorf("expected fact to be saved in memory")
	}
}

func TestSaveMemoryToolReportsOverwrite(t *testing.T) {
	mem := NewWorkingMemory()
	mem.SaveEntry("test_key", MemoryEntry{Statement: "old_fact", Status: "inferred", CreatedBy: "test"})
	tool := &SaveMemoryTool{Memory: mem, EmitFunc: func(RuntimeEvent) {}}

	res, err := tool.Execute([]byte(`{"key":"test_key","statement":"new_fact","status":"assumed"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(res), &payload); err != nil {
		t.Fatalf("expected overwrite json payload: %v", err)
	}
	if payload["overwrote_existing"] != true {
		t.Fatalf("expected overwrite flag, got %#v", payload["overwrote_existing"])
	}
	if mem.EntrySnapshot()["test_key"].Statement != "new_fact" {
		t.Fatalf("expected entry overwrite, got %#v", mem.EntrySnapshot()["test_key"])
	}
}

func TestSaveMemoryToolEmitsUpdate(t *testing.T) {
	mem := NewWorkingMemory()
	var emitted bool
	tool := &SaveMemoryTool{
		Memory: mem,
		EmitFunc: func(event RuntimeEvent) {
			if event.Type != EventStateMemoryUpdated {
				t.Fatalf("unexpected event type: %s", event.Type)
			}
			payload, ok := event.Data.(MemoryUpdatedData)
			if !ok {
				t.Fatalf("unexpected payload type: %T", event.Data)
			}
			if payload.Entries["alpha"].Statement != "beta" {
				t.Fatalf("expected emitted entries to contain saved value")
			}
			emitted = true
		},
	}

	if _, err := tool.Execute([]byte(`{"key":"alpha","statement":"beta","status":"inferred"}`)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !emitted {
		t.Fatalf("expected memory update event to be emitted")
	}
}

func TestManageSubgoalsTool(t *testing.T) {
	sm := NewSubgoalManager()
	tool := &ManageSubgoalsTool{Subgoals: sm, EmitFunc: func(RuntimeEvent) {}}

	// Add
	args := []byte(`{"action": "add", "description": "new goal", "blocking": false}`)
	res, err := tool.Execute(args)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(sm.Goals) != 1 {
		t.Errorf("expected 1 goal, got %d", len(sm.Goals))
	}
	var addPayload map[string]interface{}
	if err := json.Unmarshal([]byte(res), &addPayload); err != nil {
		t.Fatalf("expected add response json payload: %v", err)
	}
	if addPayload["tool"] != "goal_manage" || addPayload["action"] != "add" {
		t.Fatalf("unexpected add payload: %#v", addPayload)
	}
	if _, exists := addPayload["can_finalize"]; exists {
		t.Fatalf("did not expect add payload to expose can_finalize: %#v", addPayload["can_finalize"])
	}
	if addPayload["blocking"] != false {
		t.Fatalf("expected explicit blocking=false to be preserved, got %#v", addPayload["blocking"])
	}

	id := sm.Goals[0].ID

	argsChild := []byte(`{"action": "add", "description": "child goal", "parent_goal_id": "` + id + `", "blocking": false}`)
	if _, err := tool.Execute(argsChild); err != nil {
		t.Errorf("unexpected error adding child goal: %v", err)
	}
	if len(sm.Goals) != 2 || sm.Goals[1].ParentGoalID != id {
		t.Errorf("expected child goal to be nested under parent")
	}

	// Complete
	argsComplete := []byte(`{"action": "complete", "goal_id": "` + id + `", "result": "done"}`)
	res, err = tool.Execute(argsComplete)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if sm.Goals[0].Status != StatusComplete {
		t.Errorf("expected status complete, got %s", sm.Goals[0].Status)
	}
	var completePayload map[string]interface{}
	if err := json.Unmarshal([]byte(res), &completePayload); err != nil {
		t.Fatalf("expected complete response json payload: %v", err)
	}
	if completePayload["tool"] != "goal_manage" || completePayload["action"] != "complete" {
		t.Fatalf("unexpected complete payload: %#v", completePayload)
	}
	if completePayload["goal_id"] != id {
		t.Fatalf("expected complete payload to keep goal_id, got %#v", completePayload["goal_id"])
	}
	if completePayload["result"] != "done" {
		t.Fatalf("expected complete payload to keep result, got %#v", completePayload["result"])
	}
	if _, exists := completePayload["can_finalize"]; exists {
		t.Fatalf("did not expect complete payload to expose can_finalize: %#v", completePayload["can_finalize"])
	}

	// Unknown Action
	argsUnknown := []byte(`{"action": "foo"}`)
	_, err = tool.Execute(argsUnknown)
	if err == nil {
		t.Errorf("expected error for unknown action")
	}
}
