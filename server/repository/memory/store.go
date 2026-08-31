package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
)

type UserRepository struct {
	mu    sync.Mutex
	users map[string]*domain.User
}

func NewUserRepository() *UserRepository {
	return &UserRepository{users: make(map[string]*domain.User)}
}

func (r *UserRepository) GetByID(ctx context.Context, userID string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.users[userID]
	if !ok {
		return nil, fmt.Errorf("%w: user %s", repository.ErrNotFound, userID)
	}
	copy := *user
	return &copy, nil
}

func (r *UserRepository) Create(ctx context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *user
	r.users[user.ID] = &copy
	return nil
}

func (r *UserRepository) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	user, ok := r.users[userID]
	if !ok {
		return fmt.Errorf("%w: user %s", repository.ErrNotFound, userID)
	}
	user.PasswordHash = passwordHash
	user.UpdatedAt = time.Now()
	return nil
}

type WorkspaceRepository struct {
	mu         sync.Mutex
	workspaces map[string]*domain.Workspace
	members    map[string]map[string]domain.WorkspaceRole
}

func NewWorkspaceRepository() *WorkspaceRepository {
	return &WorkspaceRepository{
		workspaces: make(map[string]*domain.Workspace),
		members:    make(map[string]map[string]domain.WorkspaceRole),
	}
}

func (r *WorkspaceRepository) GetByID(ctx context.Context, workspaceID string) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	workspace, ok := r.workspaces[workspaceID]
	if !ok {
		return nil, fmt.Errorf("%w: workspace %s", repository.ErrNotFound, workspaceID)
	}
	copy := *workspace
	return &copy, nil
}

func (r *WorkspaceRepository) IsMember(ctx context.Context, workspaceID, userID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	roles, ok := r.members[workspaceID]
	if !ok {
		return false, nil
	}
	_, ok = roles[userID]
	return ok, nil
}

func (r *WorkspaceRepository) GetMemberRole(ctx context.Context, workspaceID, userID string) (domain.WorkspaceRole, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	roles, ok := r.members[workspaceID]
	if !ok {
		return "", false, nil
	}
	role, ok := roles[userID]
	return role, ok, nil
}

func (r *WorkspaceRepository) ListByUser(ctx context.Context, userID string) ([]domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	workspaces := make([]domain.Workspace, 0)
	for workspaceID, roles := range r.members {
		if _, ok := roles[userID]; !ok {
			continue
		}
		if workspace, ok := r.workspaces[workspaceID]; ok {
			workspaces = append(workspaces, *workspace)
		}
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].ID < workspaces[j].ID })
	return workspaces, nil
}

func (r *WorkspaceRepository) CreateWorkspace(ctx context.Context, workspace *domain.Workspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *workspace
	r.workspaces[workspace.ID] = &copy
	return nil
}

func (r *WorkspaceRepository) AddMember(ctx context.Context, member *domain.WorkspaceMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[member.WorkspaceID]; !ok {
		r.members[member.WorkspaceID] = make(map[string]domain.WorkspaceRole)
	}
	r.members[member.WorkspaceID][member.UserID] = member.Role
	return nil
}

type FileRepository struct {
	mu       sync.Mutex
	files    map[string]*domain.File
	sessions map[string][]string
}

type ReportRepository struct {
	mu      sync.Mutex
	reports map[string]*domain.Report
}

func NewFileRepository() *FileRepository {
	return &FileRepository{
		files:    make(map[string]*domain.File),
		sessions: make(map[string][]string),
	}
}

func NewReportRepository() *ReportRepository {
	return &ReportRepository{
		reports: make(map[string]*domain.Report),
	}
}

func (r *FileRepository) Create(ctx context.Context, file *domain.File) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *file
	r.files[file.ID] = &copy
	return nil
}

func (r *FileRepository) GetByID(ctx context.Context, fileID string) (*domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	file, ok := r.files[fileID]
	if !ok {
		return nil, fmt.Errorf("%w: file %s", repository.ErrNotFound, fileID)
	}
	copy := *file
	return &copy, nil
}

func (r *FileRepository) ListBySession(ctx context.Context, sessionID string) ([]domain.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fileIDs := r.sessions[sessionID]
	files := make([]domain.File, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		if file, ok := r.files[fileID]; ok {
			files = append(files, *file)
		}
	}
	return files, nil
}

func (r *FileRepository) AttachFilesToSession(ctx context.Context, sessionID string, fileIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.sessions[sessionID] = append(r.sessions[sessionID], fileIDs...)
	return nil
}

func (r *FileRepository) Delete(ctx context.Context, fileID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.files, fileID)
	for sessionID, ids := range r.sessions {
		kept := ids[:0]
		for _, id := range ids {
			if id != fileID {
				kept = append(kept, id)
			}
		}
		r.sessions[sessionID] = kept
	}
	return nil
}

func (r *ReportRepository) Create(ctx context.Context, report *domain.Report) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *report
	r.reports[report.RunID] = &copy
	return nil
}

func (r *ReportRepository) GetByRunID(ctx context.Context, runID string) (*domain.Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.reports[runID]
	if !ok {
		return nil, fmt.Errorf("%w: report for run %s", repository.ErrNotFound, runID)
	}
	copy := *report
	return &copy, nil
}

type RunRepository struct {
	mu   sync.Mutex
	runs map[string]*domain.AnalysisRun
}

func NewRunRepository() *RunRepository {
	return &RunRepository{runs: make(map[string]*domain.AnalysisRun)}
}

func (r *RunRepository) Create(ctx context.Context, run *domain.AnalysisRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *run
	if copy.RunKind == "" {
		copy.RunKind = domain.RunKindRoot
	}
	r.runs[run.ID] = &copy
	return nil
}

func (r *RunRepository) GetByID(ctx context.Context, runID string) (*domain.AnalysisRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return nil, fmt.Errorf("%w: run %s", repository.ErrNotFound, runID)
	}
	copy := *run
	return &copy, nil
}

func (r *RunRepository) ListBySession(ctx context.Context, sessionID string, limit int) ([]domain.AnalysisRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit == 0 {
		limit = 20
	}
	initialCap := limit
	if initialCap < 0 {
		initialCap = 0
	}
	runs := make([]domain.AnalysisRun, 0, initialCap)
	for _, run := range r.runs {
		if run.SessionID == sessionID && (run.ParentRunID == nil || *run.ParentRunID == "") {
			runs = append(runs, *run)
		}
	}
	if len(runs) > 1 {
		sort.Slice(runs, func(i, j int) bool {
			return runs[i].CreatedAt.After(runs[j].CreatedAt)
		})
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (r *RunRepository) ListByParent(ctx context.Context, parentRunID string) ([]domain.AnalysisRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := make([]domain.AnalysisRun, 0, 8)
	for _, run := range r.runs {
		if run.ParentRunID != nil && *run.ParentRunID == parentRunID {
			runs = append(runs, *run)
		}
	}
	if len(runs) > 1 {
		sort.Slice(runs, func(i, j int) bool {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		})
	}
	return runs, nil
}

func (r *RunRepository) UpdateStatus(ctx context.Context, runID string, status domain.RunStatus, errMsg *string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return fmt.Errorf("%w: run %s", repository.ErrNotFound, runID)
	}
	run.Status = status
	run.ErrorMessage = errMsg
	if status == domain.RunStatusCompleted || status == domain.RunStatusCancelled || status == domain.RunStatusFailed {
		now := time.Now()
		run.FinishedAt = &now
	}
	return nil
}

func (r *RunRepository) UpdateSummary(ctx context.Context, runID, summary string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return fmt.Errorf("%w: run %s", repository.ErrNotFound, runID)
	}
	run.Summary = summary
	return nil
}

func (r *RunRepository) BindReportFile(ctx context.Context, runID, reportFileID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[runID]
	if !ok {
		return fmt.Errorf("%w: run %s", repository.ErrNotFound, runID)
	}
	run.ReportFileID = &reportFileID
	return nil
}
