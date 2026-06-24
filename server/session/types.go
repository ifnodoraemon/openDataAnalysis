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
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
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
	FinalizeTool  *tools.FinalizeReportTool
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

	// 当 LLM 配置可用时，将语义预分析器注入 Ingester，导入文件后自动触发
	if config.Cfg != nil && config.Cfg.LLMAPIKey != "" {
		llmClient := agent.NewLLMClient()
		ingester.SemanticEnricher = llmClient.SimpleChatFunc()
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

	ctx := tools.ToolContext{
		Ingester:    s.Ingester,
		ReportState: s.ReportState,
		EditState:   s.EditState,
		Memory:      memory,
		Subgoals:    subgoals,
		SessionID:   id,
		WorkspaceID: workspaceID,
		SessionSourcesProvider: func() ([]service.SessionSourceSummary, error) {
			if sourceService == nil {
				return nil, nil
			}
			return sourceService.GetSessionSources(context.Background(), id)
		},
		ProfileDetailProvider: func(profileID string) (string, string, error) {
			if sourceService == nil {
				return "{}", "[]", nil
			}
			profile, confirmations, err := sourceService.GetSessionProfileDetail(context.Background(), id, profileID)
			if err != nil {
				return "{}", "[]", err
			}
			confJSON, _ := json.Marshal(confirmations)
			return profile.ProfileJSON, string(confJSON), nil
		},
		GovernanceProvider: func() (service.GovernanceInspection, error) {
			if sourceService == nil {
				return service.GovernanceInspection{}, nil
			}
			return sourceService.InspectSessionGovernance(context.Background(), workspaceID, id)
		},
		ConfirmedOverridesProvider: func(tableName string) map[string]interface{} {
			if sourceService == nil {
				return nil
			}
			profiles, err := sourceService.GetSessionProfiles(context.Background(), id)
			if err != nil {
				return nil
			}
			for _, p := range profiles {
				if p.AnalysisTableName == tableName && p.ProfileStatus == string(domain.ProfileStatusConfirmed) {
					fullProfile, _, err := sourceService.GetProfileDetail(context.Background(), p.ProfileID)
					if err != nil {
						continue
					}
					var profile map[string]interface{}
					if err := json.Unmarshal([]byte(fullProfile.ProfileJSON), &profile); err == nil {
						overrides := make(map[string]interface{})
						if pt, ok := profile["primary_time_column"]; ok {
							overrides["primary_time_column"] = pt
						}
						if cm, ok := profile["confirmed_metric_mappings"]; ok {
							overrides["confirmed_metric_mappings"] = cm
						}
						if cj, ok := profile["confirmed_join_candidates"]; ok {
							overrides["confirmed_join_candidates"] = cj
						}
						if ua, ok := profile["unit_annotations"]; ok {
							overrides["unit_annotations"] = ua
						}
						return overrides
					}
				}
			}
			return nil
		},
		KnownRowCount: func(tableName string) (int, bool) {
			if sourceService == nil {
				return 0, false
			}
			sources, err := sourceService.GetSessionSources(context.Background(), id)
			if err != nil {
				return 0, false
			}
			for _, src := range sources {
				if src.AnalysisTableName == tableName {
					return src.RowCount, true
				}
			}
			return 0, false
		},
		QueryLocker: s,
		ProfileConfirmer: func(profileID, confirmedBy, scope, overridesJSON string) error {
			if sourceService == nil {
				return fmt.Errorf("source service not available")
			}
			_, err := sourceService.ConfirmProfile(context.Background(), profileID, workspaceID, id, confirmedBy, scope, overridesJSON)
			return err
		},
		Now: time.Now,
	}

	masterReg := tools.NewRegistry()
	masterReg.LoadGlobalTools(ctx)

	// 主控和子 Agent 使用同一套工具语义；子 Agent 只是按本次任务裁剪过工具边界的递归实例。
	runtimeAllowed := []string{
		"state_time_context_inspect",
		"state_session_sources_inspect",
		"state_semantic_profile_inspect",
		"state_governance_inspect",
		"data_list_tables",
		"data_describe_table",
		"data_query_sql",
		"report_create_chart",
		"report_manage_blocks",
		"report_configure_layout",
		"report_finalize",
		"memory_save_fact",
		"state_memory_inspect",
		"state_goal_inspect",
		"state_report_inspect",
		"state_report_edit_inspect",
		"state_source_confirm_profile",
		"goal_manage",
		"user_request_input",
		"task_delegate",
	}
	if pt, err := masterReg.Get("code_run_python"); err == nil {
		if runPython, ok := pt.(*tools.RunPythonTool); ok {
			runPython.MCPEndpoint = config.Cfg.PythonMCPURL
			if err := runPython.HealthCheck(context.Background()); err != nil {
				log.Printf("code_run_python disabled for session %s: %v", id, err)
			} else {
				runtimeAllowed = append(runtimeAllowed, "code_run_python")
			}
		}
	}
	var buildRuntimeRegistry func([]string) *tools.Registry
	buildRuntimeRegistry = func(allowed []string) *tools.Registry {
		reg := tools.NewRegistry()
		reg.LoadGlobalTools(ctx)
		if len(allowed) > 0 {
			reg = reg.CloneFiltered(allowed)
		}
		if dt, err := reg.Get("task_delegate"); err == nil {
			if dtTool, ok := dt.(*agent.DelegateTaskTool); ok {
				dtTool.RegistryFactory = buildRuntimeRegistry
				dtTool.Subgoals = subgoals
				dtTool.Memory = memory
			}
		}
		return reg
	}
	runtimeRegistry := buildRuntimeRegistry(runtimeAllowed)

	s.Registry = runtimeRegistry
	if ft, err := runtimeRegistry.Get("report_finalize"); err == nil {
		if ftt, ok := ft.(*tools.FinalizeReportTool); ok {
			s.FinalizeTool = ftt
		}
	}

	policyPrompt := agent.BuildPolicyPrompt()
	s.Engine = agent.NewEngine(runtimeRegistry, policyPrompt)

	return s, nil
}

func (s *Session) Touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastSeenAt = time.Now()
}

func (s *Session) FilesForClient() []agent.UploadedFile {
	files, err := s.FileService.GetSessionFiles(context.Background(), s.ID)
	if err != nil {
		return nil
	}

	clientFiles := make([]agent.UploadedFile, 0, len(files))
	for _, file := range files {
		clientFiles = append(clientFiles, agent.UploadedFile{
			FileID: file.ID,
			Name:   file.DisplayName,
			Size:   file.SizeBytes,
		})
	}
	return clientFiles
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ActiveRun == nil || s.ActiveRun.Status != "waiting_user_input" {
		return ""
	}
	runID := s.ActiveRun.RunID
	s.ActiveRun.Status = "running"
	s.LastSeenAt = time.Now()
	return runID
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
	s.EditState.Mode = strings.TrimSpace(edit.Mode)
	s.EditState.TargetRunID = strings.TrimSpace(edit.TargetRunID)
	s.EditState.TargetBlockID = strings.TrimSpace(edit.BlockID)
	s.EditState.TargetBlockLabel = strings.TrimSpace(edit.BlockLabel)
	s.EditState.TargetChartID = strings.TrimSpace(edit.ChartID)
	s.EditState.SelectionText = strings.TrimSpace(edit.SelectionText)
	s.EditState.SelectionStart = edit.SelectionStart
	s.EditState.SelectionEnd = edit.SelectionEnd
	s.EditState.SelectionRangeSet = edit.SelectionRangeSet
	s.EditState.PreserveOtherBlocks = edit.PreserveOtherBlocks
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
		Mode:                s.EditState.Mode,
		TargetRunID:         s.EditState.TargetRunID,
		BlockID:             s.EditState.TargetBlockID,
		BlockLabel:          s.EditState.TargetBlockLabel,
		ChartID:             s.EditState.TargetChartID,
		SelectionText:       s.EditState.SelectionText,
		SelectionStart:      s.EditState.SelectionStart,
		SelectionEnd:        s.EditState.SelectionEnd,
		SelectionRangeSet:   s.EditState.SelectionRangeSet,
		PreserveOtherBlocks: s.EditState.PreserveOtherBlocks,
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
			Mode:                s.EditState.Mode,
			TargetRunID:         s.EditState.TargetRunID,
			BlockID:             s.EditState.TargetBlockID,
			BlockLabel:          s.EditState.TargetBlockLabel,
			ChartID:             s.EditState.TargetChartID,
			SelectionText:       s.EditState.SelectionText,
			SelectionStart:      s.EditState.SelectionStart,
			SelectionEnd:        s.EditState.SelectionEnd,
			SelectionRangeSet:   s.EditState.SelectionRangeSet,
			PreserveOtherBlocks: s.EditState.PreserveOtherBlocks,
		},
	}
}

func (s *Session) LoadReportSnapshot(snapshot *domain.ReportSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot == nil {
		return
	}
	s.ReportState.Lock()
	s.ReportState.FinalTitle = strings.TrimSpace(snapshot.Title)
	s.ReportState.FinalAuthor = strings.TrimSpace(snapshot.Author)
	s.ReportState.Layout = tools.ReportLayout{
		CustomCSS: snapshot.Layout.CustomCSS,
		BodyClass: snapshot.Layout.BodyClass,
	}
	s.ReportState.NeedsFinalize = snapshot.NeedsFinalize
	s.ReportState.FinalizeAttempts = 0
	s.ReportState.LastFinalizeIssueSignature = ""
	s.ReportState.Blocks = make([]tools.ReportBlock, 0, len(snapshot.Blocks))
	for _, block := range snapshot.Blocks {
		rb := tools.ReportBlock{
			ID:      block.ID,
			Kind:    block.Kind,
			Title:   block.Title,
			Content: block.Content,
			ChartID: block.ChartID,
		}
		if len(block.Sources) > 0 {
			var sources []tools.EvidenceRef
			if err := json.Unmarshal(block.Sources, &sources); err == nil {
				rb.Sources = sources
			}
		}
		s.ReportState.Blocks = append(s.ReportState.Blocks, rb)
	}
	s.ReportState.Charts = make([]tools.ChartData, 0, len(snapshot.Charts))
	for _, chart := range snapshot.Charts {
		s.ReportState.Charts = append(s.ReportState.Charts, tools.ChartData{
			ID:     chart.ID,
			Option: chart.Option,
			Width:  chart.Width,
			Height: chart.Height,
		})
	}
	s.ReportState.Unlock()
	if s.EditState != nil {
		s.ReportState.RLock()
		s.EditState.RefreshFromReportState(s.ReportState)
		s.ReportState.RUnlock()
	}
}

func (s *Session) PrepareUserRun(ctx context.Context, userMsg agent.UserMessage, loader ReportSnapshotLoader) error {
	var snapshot *domain.ReportSnapshot
	targetRunID := ""
	if userMsg.EditContext != nil && strings.TrimSpace(userMsg.EditContext.TargetRunID) != "" {
		targetRunID = strings.TrimSpace(userMsg.EditContext.TargetRunID)
	} else if userMsg.TurnContext != nil && strings.TrimSpace(userMsg.TurnContext.ReportTargetRunID) != "" {
		targetRunID = strings.TrimSpace(userMsg.TurnContext.ReportTargetRunID)
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
		s.LoadReportSnapshot(snapshot)
	}
	s.ConfigureEditState(userMsg.EditContext)
	return nil
}

func (s *Session) Reset(keepFiles bool) error {
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
	s.ReportState.FinalizeAttempts = 0
	s.ReportState.LastFinalizeIssueSignature = ""
	s.ReportState.Unlock()
	if s.EditState != nil {
		s.EditState.Reset()
	}
	s.LastSeenAt = time.Now()
	s.mu.Unlock()

	if err := s.Ingester.ResetDB(s.ID); err != nil {
		return err
	}

	// 文件元数据已经通过 FileRepository 与 session 关联，当前阶段 keepFiles=false 仅重置分析状态。
	_ = keepFiles
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

func (s *Session) RuntimeState() (map[string]string, []agent.Subgoal) {
	var memory map[string]string
	var subgoals []agent.Subgoal
	if s.Memory != nil {
		memory = s.Memory.Snapshot()
	}
	if s.Subgoals != nil {
		subgoals = s.Subgoals.ListAll()
	}
	return memory, subgoals
}

func (s *Session) LoadRuntimeState(memory map[string]string, subgoals []agent.Subgoal) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Memory != nil {
		s.Memory.ReplaceSnapshot(memory)
	}
	if s.Subgoals != nil {
		s.Subgoals.ReplaceAll(subgoals)
	}
	s.LastSeenAt = time.Now()
}

func (s *Session) RuntimeVars() []agent.RuntimeContextBlock {
	var vars []agent.RuntimeContextBlock
	s.mu.Lock()
	defer s.mu.Unlock()

	// Active edit scope is explicit UI/turn state. Broader report and goal facts
	// stay pull-based through state_* inspection tools.
	if s.EditState != nil && s.EditState.Active() {
		content := fmt.Sprintf("Mode: %s\nScopeKind: %s\n", s.EditState.Mode, s.EditState.ScopeKind())
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
		if s.EditState.PreserveOtherBlocks {
			content += "PreserveOtherBlocks: true\n"
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
