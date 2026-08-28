package session

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type RunState struct {
	RunID     string
	Status    string
	Cancel    context.CancelFunc
	StartedAt time.Time
}

type ReportSnapshotLoader interface {
	LoadReportSnapshot(ctx context.Context, sessionID, workspaceID, userID, runID string) (*domain.ReportSnapshot, error)
}

type Session struct {
	ID            string
	WorkspaceID   string
	UserID        string
	CacheRoot     string
	FileService   *service.FileService
	SourceService *service.SourceService
	Ingester      *data.Ingester
	Registry      *tools.Registry
	Engine        *agent.Engine
	ReportState   *tools.ReportState
	EditState     *tools.ReportEditState
	Memory        *agent.WorkingMemory
	Subgoals      *agent.SubgoalManager
	ActiveRun     *RunState
	CreatedAt     time.Time
	LastSeenAt    time.Time
	mu            sync.Mutex
	uploadMu      sync.RWMutex
}

func New(id, workspaceID, userID, cacheRoot string, fileService *service.FileService, sourceService *service.SourceService) (*Session, error) {
	ingester := data.NewIngester(cacheRoot)
	if err := ingester.InitDB(id); err != nil {
		return nil, err
	}

	memory := agent.NewWorkingMemory()
	subgoals := agent.NewSubgoalManager()

	s := &Session{
		ID:            id,
		WorkspaceID:   workspaceID,
		UserID:        userID,
		CacheRoot:     cacheRoot,
		FileService:   fileService,
		SourceService: sourceService,
		Ingester:      ingester,
		ReportState:   &tools.ReportState{},
		EditState:     &tools.ReportEditState{},
		Memory:        memory,
		Subgoals:      subgoals,
		CreatedAt:     time.Now(),
		LastSeenAt:    time.Now(),
	}

	var buildRuntimeRegistry tools.RegistryFactory
	var availableRuntimeTools map[string]struct{}
	ctx := tools.ToolContext{
		Ingester:    s.Ingester,
		ReportState: s.ReportState,
		EditState:   s.EditState,
		Memory:      memory,
		Subgoals:    subgoals,
		SessionID:   id,
		WorkspaceID: workspaceID,
		FileService: fileService,
		QueryLocker: s,
		Now:         time.Now,
	}
	if sourceService != nil {
		ctx.SessionSourcesProvider = func(ctx context.Context) ([]service.SessionSourceSummary, error) {
			return sourceService.GetSessionSources(ctx, id)
		}
		ctx.ProfileDetailProvider = func(ctx context.Context, profileID string) (string, string, string, error) {
			profile, confirmations, err := sourceService.GetSessionProfileDetail(ctx, id, profileID)
			if err != nil {
				return "{}", "[]", "[]", err
			}
			confJSON, err := json.Marshal(confirmations)
			if err != nil {
				return "{}", "[]", "[]", fmt.Errorf("encode profile confirmations: %w", err)
			}
			assets, err := sourceService.GetSemanticAssets(ctx, workspaceID, profile.SchemaSignature)
			if err != nil {
				return "{}", "[]", "[]", err
			}
			assetsJSON, err := json.Marshal(assets)
			if err != nil {
				return "{}", "[]", "[]", fmt.Errorf("encode reusable profile patches: %w", err)
			}
			return profile.ProfileJSON, string(confJSON), string(assetsJSON), nil
		}
		ctx.GovernanceProvider = func(ctx context.Context) (service.GovernanceInspection, error) {
			return sourceService.InspectSessionGovernance(ctx, workspaceID, id)
		}
		ctx.ProfileConfirmer = func(ctx context.Context, profileID, scope, overridesJSON, confirmationReceiptID string) ([]string, error) {
			if s.Engine == nil {
				return nil, fmt.Errorf("agent engine is not initialized")
			}
			authorizationPayload, err := semanticConfirmationAuthorizationPayload(scope, overridesJSON)
			if err != nil {
				return nil, err
			}
			var auditErrors []string
			err = s.Engine.CommitWithConfirmationReceipt(confirmationReceiptID, "profile_patch_commit", profileID, authorizationPayload, func(actorUserID string) error {
				_, auditErrors, err = sourceService.ConfirmProfile(ctx, profileID, workspaceID, id, actorUserID, scope, overridesJSON, confirmationReceiptID, domain.ConfirmationProvenanceAuthorizationReceipt)
				return err
			})
			return auditErrors, err
		}
		ctx.LiveQueryProvider = func(ctx context.Context, req tools.LiveQueryRequest) (*tools.LiveQueryResult, error) {
			rows, err := sourceService.ExecuteSessionLiveQuery(ctx, service.LiveQueryCall{
				SourceID:       req.SourceID,
				SessionID:      id,
				WorkspaceID:    workspaceID,
				SQL:            req.SQL,
				TimeoutSeconds: req.TimeoutSeconds,
			})
			if err != nil {
				return nil, err
			}
			return &tools.LiveQueryResult{
				SourceID: req.SourceID,
				Dialect:  rows.Dialect,
				Columns:  rows.Columns,
				Rows:     rows.Rows,
				RowCount: len(rows.Rows),
			}, nil
		}
		ctx.LiveTablesProvider = func(ctx context.Context, sourceID string) ([]tools.LiveSourceTable, error) {
			facts, err := sourceService.ListSessionLiveTables(ctx, id, workspaceID, sourceID)
			if err != nil {
				return nil, err
			}
			tables := make([]tools.LiveSourceTable, 0, len(facts))
			for _, fact := range facts {
				tables = append(tables, tools.LiveSourceTable{
					Schema:           fact.Schema,
					Name:             fact.Name,
					QualifiedName:    fact.QualifiedName,
					Kind:             fact.Kind,
					RowCountEstimate: fact.RowCountEstimate,
					Estimated:        fact.Estimated,
					ProfileID:        fact.ProfileID,
					SnapshotID:       fact.SnapshotID,
					Dialect:          fact.Dialect,
				})
			}
			return tables, nil
		}
		ctx.LiveTableDescribeProvider = func(ctx context.Context, sourceID, schema, name string, sampleRows int) (*tools.LiveTableDescription, error) {
			description, err := sourceService.DescribeSessionLiveTable(ctx, service.LiveDescribeCall{
				SourceID:    sourceID,
				SessionID:   id,
				WorkspaceID: workspaceID,
				Schema:      schema,
				Name:        name,
				SampleRows:  sampleRows,
			})
			if err != nil {
				return nil, err
			}
			columns := make([]tools.LiveColumnFacts, 0, len(description.Columns))
			for _, column := range description.Columns {
				columns = append(columns, tools.LiveColumnFacts{Name: column.Name, DeclaredType: column.DeclaredType})
			}
			result := &tools.LiveTableDescription{
				SourceID:         description.SourceID,
				Schema:           description.Schema,
				Name:             description.Name,
				QualifiedName:    description.QualifiedName,
				Dialect:          description.Dialect,
				RowCountEstimate: description.RowCountEstimate,
				Estimated:        description.Estimated,
				ColumnCount:      description.ColumnCount,
				Columns:          columns,
				SampleRows:       description.SampleRows,
				Warnings:         description.Warnings,
			}
			if description.Sample != nil {
				result.Sample = &tools.LiveQueryResult{
					SourceID: description.SourceID,
					Dialect:  description.Sample.Dialect,
					Columns:  description.Sample.Columns,
					Rows:     description.Sample.Rows,
					RowCount: len(description.Sample.Rows),
				}
			}
			return result, nil
		}
	}
	buildRuntimeRegistry = func(allowed []string) *tools.Registry {
		reg := tools.NewRegistry()
		reg.LoadGlobalTools(ctx)
		delegable := make(map[string]struct{})
		for _, name := range reg.RuntimeToolNames(true) {
			delegable[name] = struct{}{}
		}
		filtered := make([]string, 0, len(allowed))
		for _, name := range allowed {
			_, isDelegable := delegable[name]
			_, isAvailable := availableRuntimeTools[name]
			if isDelegable && isAvailable {
				filtered = append(filtered, name)
			}
		}
		return reg.CloneFiltered(filtered)
	}
	ctx.DelegateRegistryFactory = buildRuntimeRegistry

	masterReg := tools.NewRegistry()
	masterReg.LoadGlobalTools(ctx)

	// Runtime visibility comes from each tool's capability contract.
	runtimeAllowed := masterReg.RuntimeToolNames(false)
	for _, candidate := range masterReg.ListTools() {
		if checker, ok := candidate.(tools.AvailabilityTool); ok {
			if err := checker.CheckAvailability(context.Background()); err != nil {
				log.Printf("tool %s disabled for session %s: %v", candidate.Name(), id, err)
				runtimeAllowed = withoutToolName(runtimeAllowed, candidate.Name())
			}
		}
	}
	availableRuntimeTools = make(map[string]struct{}, len(runtimeAllowed))
	for _, name := range runtimeAllowed {
		availableRuntimeTools[name] = struct{}{}
	}
	runtimeRegistry := masterReg.CloneFiltered(runtimeAllowed)

	s.Registry = runtimeRegistry

	s.Engine = agent.NewEngine(runtimeRegistry)
	s.Engine.SetReportState(s.ReportState)

	return s, nil
}

func semanticConfirmationAuthorizationPayload(scope, overridesJSON string) (string, error) {
	if scope == "" || scope != strings.TrimSpace(scope) {
		return "", fmt.Errorf("scope must be a non-empty exact value")
	}
	if overridesJSON == "" || overridesJSON != strings.TrimSpace(overridesJSON) {
		return "", fmt.Errorf("overrides_json must be a non-empty exact JSON object")
	}
	var overrides map[string]interface{}
	if err := jsoncontract.Decode([]byte(overridesJSON), &overrides); err != nil {
		return "", fmt.Errorf("overrides_json must be a strict JSON object: %w", err)
	}
	payload, err := json.Marshal(map[string]interface{}{"scope": scope, "overrides": overrides})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func withoutToolName(names []string, excluded string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if name != excluded {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func (s *Session) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSeenAt = time.Now()
}

func (s *Session) StartRun(parent context.Context) (string, context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ActiveRun != nil {
		return "", nil, fmt.Errorf("a task is still running or not yet cleaned up, please wait and try again after stopping")
	}

	runID := "r_" + uuid.New().String()[:8]
	ctx, cancel := context.WithCancel(parent)
	s.ActiveRun = &RunState{
		RunID:     runID,
		Status:    "running",
		Cancel:    cancel,
		StartedAt: time.Now(),
	}
	s.LastSeenAt = time.Now()
	return runID, ctx, nil
}

func (s *Session) FinishRun(runID, status string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	finished := false
	if s.ActiveRun != nil && s.ActiveRun.RunID == runID {
		s.ActiveRun.Status = status
		s.ActiveRun.Cancel = nil
		s.ActiveRun = nil
		if s.EditState != nil {
			s.EditState.Reset()
		}
		s.LastSeenAt = time.Now()
		finished = true
	}
	return finished
}

func (s *Session) LockUpload() {
	s.uploadMu.Lock()
}

func (s *Session) UnlockUpload() {
	s.uploadMu.Unlock()
}

func (s *Session) RLockQuery() {
	s.uploadMu.RLock()
}

func (s *Session) RUnlockQuery() {
	s.uploadMu.RUnlock()
}

func (s *Session) SuspendRun(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun != nil && s.ActiveRun.RunID == runID {
		s.ActiveRun.Status = "waiting_user_input"
		s.LastSeenAt = time.Now()
		return true
	}
	return false
}

func (s *Session) ResumeRun(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun != nil && s.ActiveRun.RunID == runID {
		s.ActiveRun.Status = "running"
	}
	s.LastSeenAt = time.Now()
}

// UpdateCancelFunc 更新当活跃任务的 cancel 控制流
func (s *Session) UpdateCancelFunc(runID string, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun != nil && s.ActiveRun.RunID == runID {
		s.ActiveRun.Cancel = cancel
		return true
	}
	return false
}

// GetWaitingRunID 返回正在等待用户输入的 run ID。
func (s *Session) GetWaitingRunID() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun != nil && s.ActiveRun.Status == "waiting_user_input" {
		return s.ActiveRun.RunID, true
	}
	return "", false
}

// ConsumeWaitingRun 原子地检查并清除 waiting_user_input 状态。
// 若当前确实处于等待状态，则将状态改为 running 并返回 runID。
// 返回 empty string 表示当前不处于等待状态（已被其它 goroutine 消费）。
// 用于替代原来的 GetWaitingRunID + ResumeRun 两步操作，将第二次重复提交的竞态消除。
func (s *Session) ConsumeWaitingRun() string {
	return s.ConsumeWaitingRunExact("")
}

func (s *Session) ConsumeWaitingRunExact(expectedRunID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun == nil || s.ActiveRun.Status != "waiting_user_input" {
		return ""
	}
	if expectedRunID != "" && s.ActiveRun.RunID != expectedRunID {
		return ""
	}
	runID := s.ActiveRun.RunID
	s.ActiveRun.Status = "running"
	s.LastSeenAt = time.Now()
	return runID
}

func (s *Session) ReturnRunToWaiting(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun == nil || s.ActiveRun.RunID != runID || s.ActiveRun.Status != "running" {
		return false
	}
	s.ActiveRun.Status = "waiting_user_input"
	s.LastSeenAt = time.Now()
	return true
}

func (s *Session) CancelRun(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ActiveRun == nil {
		return false
	}
	if runID != "" && s.ActiveRun.RunID != runID {
		return false
	}
	if s.ActiveRun.Cancel != nil {
		s.ActiveRun.Status = "cancelling"
		s.ActiveRun.Cancel()
	}
	s.LastSeenAt = time.Now()
	return true
}

func (s *Session) WaitUntilIdle(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		idle := s.ActiveRun == nil
		s.mu.Unlock()
		if idle {
			return true
		}
		if timeout <= 0 || time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Session) ConfigureEditState(edit *agent.ReportEditContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EditState == nil {
		s.EditState = &tools.ReportEditState{}
	}
	if edit == nil {
		s.EditState.Reset()
		return
	}
	s.EditState.ScopeKindValue = edit.ScopeKind
	s.EditState.TargetRunID = edit.TargetRunID
	s.EditState.TargetBlockID = edit.BlockID
	s.EditState.TargetBlockLabel = edit.BlockLabel
	s.EditState.TargetChartID = edit.ChartID
	s.EditState.SelectionText = edit.SelectionText
	s.EditState.SelectionStart = edit.SelectionStart
	s.EditState.SelectionEnd = edit.SelectionEnd
	s.EditState.SelectionRangeSet = edit.SelectionRangeSet
	s.EditState.RefreshFromReportState(s.ReportState)
}

func (s *Session) ClearEditState() {
	s.ConfigureEditState(nil)
}

func (s *Session) CurrentEditContext() *agent.ReportEditContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EditState == nil || !s.EditState.Active() {
		return nil
	}
	return &agent.ReportEditContext{
		ScopeKind:         s.EditState.ScopeKindValue,
		TargetRunID:       s.EditState.TargetRunID,
		BlockID:           s.EditState.TargetBlockID,
		BlockLabel:        s.EditState.TargetBlockLabel,
		ChartID:           s.EditState.TargetChartID,
		SelectionText:     s.EditState.SelectionText,
		SelectionStart:    s.EditState.SelectionStart,
		SelectionEnd:      s.EditState.SelectionEnd,
		SelectionRangeSet: s.EditState.SelectionRangeSet,
	}
}

func (s *Session) CurrentEditStateData() *agent.EditStateUpdatedData {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EditState == nil || !s.EditState.Active() {
		return &agent.EditStateUpdatedData{Active: false}
	}
	return &agent.EditStateUpdatedData{
		Active:    true,
		ScopeKind: s.EditState.ScopeKind(),
		EditContext: &agent.ReportEditContext{
			ScopeKind:         s.EditState.ScopeKindValue,
			TargetRunID:       s.EditState.TargetRunID,
			BlockID:           s.EditState.TargetBlockID,
			BlockLabel:        s.EditState.TargetBlockLabel,
			ChartID:           s.EditState.TargetChartID,
			SelectionText:     s.EditState.SelectionText,
			SelectionStart:    s.EditState.SelectionStart,
			SelectionEnd:      s.EditState.SelectionEnd,
			SelectionRangeSet: s.EditState.SelectionRangeSet,
		},
	}
}

func (s *Session) LoadReportSnapshot(snapshot *domain.ReportSnapshot) error {
	if snapshot == nil {
		return nil
	}
	results, err := tools.DecodeAnalysisResults(snapshot.Results)
	if err != nil {
		return fmt.Errorf("decode report analysis results: %w", err)
	}
	artifacts, err := tools.DecodeArtifactRecords(snapshot.Artifacts)
	if err != nil {
		return fmt.Errorf("decode report artifacts: %w", err)
	}
	blocks := make([]tools.ReportBlock, 0, len(snapshot.Blocks))
	seenBlockIDs := make(map[string]struct{}, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		if block.ID == "" || block.ID != strings.TrimSpace(block.ID) {
			return fmt.Errorf("report snapshot contains a non-exact block ID")
		}
		if _, exists := seenBlockIDs[block.ID]; exists {
			return fmt.Errorf("report snapshot block ID %q is duplicated", block.ID)
		}
		seenBlockIDs[block.ID] = struct{}{}
		switch block.Kind {
		case "markdown", "html":
			if strings.TrimSpace(block.Content) == "" || block.ChartID != "" {
				return fmt.Errorf("report snapshot block %s has invalid %s structure", block.ID, block.Kind)
			}
		case "chart":
			if block.ChartID == "" || block.ChartID != strings.TrimSpace(block.ChartID) {
				return fmt.Errorf("report snapshot chart block %s requires an exact chart ID", block.ID)
			}
		default:
			return fmt.Errorf("report snapshot block %s has invalid kind %q", block.ID, block.Kind)
		}
		sources, err := tools.DecodeEvidenceRefs(block.Sources)
		if err != nil {
			return fmt.Errorf("decode report block %s sources: %w", block.ID, err)
		}
		blocks = append(blocks, tools.ReportBlock{ID: block.ID, Kind: block.Kind, Title: block.Title, Content: block.Content, ChartID: block.ChartID, Sources: sources})
	}
	charts := make([]tools.ChartData, 0, len(snapshot.Charts))
	seenChartIDs := make(map[string]struct{}, len(snapshot.Charts))
	for _, chart := range snapshot.Charts {
		if chart.ID == "" || chart.ID != strings.TrimSpace(chart.ID) {
			return fmt.Errorf("report snapshot contains a non-exact chart ID")
		}
		if _, exists := seenChartIDs[chart.ID]; exists {
			return fmt.Errorf("report snapshot chart ID %q is duplicated", chart.ID)
		}
		seenChartIDs[chart.ID] = struct{}{}
		var option map[string]interface{}
		if err := jsoncontract.Decode(chart.Option, &option); err != nil {
			return fmt.Errorf("report snapshot chart %s has invalid option JSON: %w", chart.ID, err)
		}
		if option == nil {
			return fmt.Errorf("report snapshot chart %s option must be a JSON object", chart.ID)
		}
		sources, err := tools.DecodeEvidenceRefs(chart.Sources)
		if err != nil {
			return fmt.Errorf("decode report chart %s sources: %w", chart.ID, err)
		}
		charts = append(charts, tools.ChartData{ID: chart.ID, Option: append(json.RawMessage(nil), chart.Option...), Width: chart.Width, Height: chart.Height, Sources: sources})
	}
	layout := tools.ReportLayout{
		CustomCSS: snapshot.Layout.CustomCSS,
		BodyClass: snapshot.Layout.BodyClass,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReportState.Lock()
	s.ReportState.FinalTitle = snapshot.Title
	s.ReportState.FinalAuthor = snapshot.Author
	s.ReportState.Layout = layout
	s.ReportState.NeedsFinalize = snapshot.NeedsFinalize
	s.ReportState.Results = results
	s.ReportState.Artifacts = artifacts
	s.ReportState.Blocks = blocks
	s.ReportState.Charts = charts
	s.ReportState.Unlock()
	if s.EditState != nil {
		s.EditState.RefreshFromReportState(s.ReportState)
	}
	return nil
}

func (s *Session) PrepareUserRun(ctx context.Context, userMsg agent.UserMessage, loader ReportSnapshotLoader) error {
	if err := userMsg.Validate(); err != nil {
		return err
	}
	var snapshot *domain.ReportSnapshot
	targetRunID := ""
	if userMsg.EditContext != nil && userMsg.EditContext.TargetRunID != "" {
		targetRunID = userMsg.EditContext.TargetRunID
	} else if userMsg.TurnContext != nil && userMsg.TurnContext.ReportTargetRunID != "" {
		targetRunID = userMsg.TurnContext.ReportTargetRunID
	}
	if targetRunID != "" {
		if loader == nil {
			return fmt.Errorf("missing report snapshot loader")
		}
		loaded, err := loader.LoadReportSnapshot(ctx, s.ID, s.WorkspaceID, s.UserID, targetRunID)
		if err != nil {
			return err
		}
		snapshot = loaded
	}
	if snapshot != nil {
		if err := s.LoadReportSnapshot(snapshot); err != nil {
			return err
		}
	}
	s.ConfigureEditState(userMsg.EditContext)
	return nil
}

func (s *Session) Reset() error {
	s.CancelRun("")
	s.Engine.ResetMessages()
	if s.Memory != nil {
		s.Memory.Reset()
	}
	if s.Subgoals != nil {
		s.Subgoals.Reset()
	}

	s.mu.Lock()
	s.ReportState.Lock()
	s.ReportState.Blocks = nil
	s.ReportState.Charts = nil
	s.ReportState.FinalTitle = ""
	s.ReportState.FinalAuthor = ""
	s.ReportState.Layout = tools.ReportLayout{}
	s.ReportState.NeedsFinalize = false
	s.ReportState.Results = nil
	s.ReportState.Artifacts = nil
	s.ReportState.Unlock()
	if s.EditState != nil {
		s.EditState.Reset()
	}
	s.LastSeenAt = time.Now()
	s.mu.Unlock()

	if err := s.Ingester.ResetDB(s.ID); err != nil {
		return err
	}

	return os.MkdirAll(s.FileService.TempDir, 0o755)
}

func (s *Session) Destroy() error {
	if s == nil {
		return nil
	}
	s.CancelRun("")
	if s.Ingester != nil {
		return s.Ingester.Destroy()
	}
	return nil
}

func (s *Session) RuntimeState() (map[string]agent.MemoryEntry, []agent.Subgoal) {
	var memory map[string]agent.MemoryEntry
	var subgoals []agent.Subgoal
	if s.Memory != nil {
		memory = s.Memory.EntrySnapshot()
	}
	if s.Subgoals != nil {
		subgoals = s.Subgoals.ListAll()
	}
	return memory, subgoals
}

func (s *Session) LoadRuntimeStateEntries(entries map[string]agent.MemoryEntry, subgoals []agent.Subgoal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Memory != nil {
		if err := s.Memory.ReplaceEntries(entries); err != nil {
			return err
		}
	}
	if s.Subgoals != nil {
		if err := s.Subgoals.ReplaceAll(subgoals); err != nil {
			return err
		}
	}
	s.LastSeenAt = time.Now()
	return nil
}

func (s *Session) RuntimeVars() []agent.RuntimeContextBlock {
	var vars []agent.RuntimeContextBlock
	s.mu.Lock()
	defer s.mu.Unlock()

	// Active edit scope is explicit UI/turn state. Broader report and goal facts
	// stay pull-based through state_* inspection tools.
	if s.EditState != nil && s.EditState.Active() {
		content := fmt.Sprintf("ScopeKind: %s\n", s.EditState.ScopeKind())
		if s.EditState.TargetBlockID != "" {
			content += fmt.Sprintf("TargetBlockID: %s\n", s.EditState.TargetBlockID)
		}
		if s.EditState.TargetBlockLabel != "" {
			content += fmt.Sprintf("TargetBlockLabel: %s\n", s.EditState.TargetBlockLabel)
		}
		if s.EditState.TargetChartID != "" {
			content += fmt.Sprintf("TargetChartID: %s\n", s.EditState.TargetChartID)
		}
		if s.EditState.SelectionText != "" {
			content += fmt.Sprintf("SelectionText: %s\n", s.EditState.SelectionText)
			if s.EditState.SelectionRangeSet && s.EditState.SelectionEnd > s.EditState.SelectionStart {
				content += fmt.Sprintf("SelectionRange: %d-%d\n", s.EditState.SelectionStart, s.EditState.SelectionEnd)
			}
		}
		if s.EditState.ScopeKindLocked() == "partial_selection" {
			content += "MutationContract: only the target block content may change; content outside the selected range, block title, block kind, chart_id, and sources remain protected.\n"
		}
		vars = append(vars, agent.RuntimeContextBlock{
			Name:    "active_edit_scope",
			Role:    "user",
			Content: strings.TrimSpace(content),
		})
	}

	return vars
}
