package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
	memoryrepo "github.com/ifnodoraemon/openDataAnalysis/repository/memory"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	localstorage "github.com/ifnodoraemon/openDataAnalysis/storage/local"
)

func newImportArtifactTestFileService(t *testing.T) *service.FileService {
	t.Helper()
	workspaceRepo := memoryrepo.NewWorkspaceRepository()
	if err := workspaceRepo.CreateWorkspace(context.Background(), &domain.Workspace{ID: "ws_1"}); err != nil {
		t.Fatal(err)
	}
	if err := workspaceRepo.AddMember(context.Background(), &domain.WorkspaceMember{WorkspaceID: "ws_1", UserID: "u_1", Role: domain.WorkspaceRoleOwner}); err != nil {
		t.Fatal(err)
	}
	return &service.FileService{Storage: localstorage.New(t.TempDir(), ""), FileRepo: memoryrepo.NewFileRepository(), WorkspaceRepo: workspaceRepo}
}

func TestImportArtifactToolRejectsInvalidArtifactID(t *testing.T) {
	tool := &ImportArtifactTool{}
	result, err := tool.Execute(json.RawMessage(`{"artifact_id":" spaced "}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "invalid_artifact_id") {
		t.Fatalf("expected invalid_artifact_id failure, got %s", result)
	}
}

func TestImportArtifactToolRequiresExecutionContext(t *testing.T) {
	tool := &ImportArtifactTool{}
	result, err := tool.Execute(json.RawMessage(`{"artifact_id":"file_1"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "missing_execution_context") {
		t.Fatalf("expected missing_execution_context failure, got %s", result)
	}
}

func TestImportArtifactToolRequiresExecutionIdentity(t *testing.T) {
	tool := &ImportArtifactTool{}
	tool.SetExecutionContext(context.Background())
	result, err := tool.Execute(json.RawMessage(`{"artifact_id":"file_1"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "missing_execution_identity") {
		t.Fatalf("expected missing_execution_identity failure, got %s", result)
	}
}

func TestImportArtifactToolRejectsNonCSVArtifact(t *testing.T) {
	fileService := newImportArtifactTestFileService(t)
	file, err := fileService.SaveArtifact(context.Background(), service.SaveArtifactInput{
		UserID: "u_1", WorkspaceID: "ws_1", SessionID: "s_1", RunID: "r_1",
		FileName: "summary.txt", ContentType: "text/plain", Body: strings.NewReader("not a csv"), Size: 9,
	})
	if err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}

	tool := &ImportArtifactTool{FileService: fileService}
	tool.SetExecutionContext(WithExecutionMetadata(context.Background(), ExecutionMetadata{UserID: "u_1", WorkspaceID: "ws_1", SessionID: "s_1", RunID: "r_1"}))
	result, err := tool.Execute(json.RawMessage(`{"artifact_id":"` + file.ID + `"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "artifact_not_csv") {
		t.Fatalf("expected artifact_not_csv failure, got %s", result)
	}
}

func TestImportArtifactToolRejectsUnknownArtifact(t *testing.T) {
	fileService := newImportArtifactTestFileService(t)
	tool := &ImportArtifactTool{FileService: fileService}
	tool.SetExecutionContext(WithExecutionMetadata(context.Background(), ExecutionMetadata{UserID: "u_1", WorkspaceID: "ws_1", SessionID: "s_1", RunID: "r_1"}))
	result, err := tool.Execute(json.RawMessage(`{"artifact_id":"file_missing"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(result, "artifact_not_found") {
		t.Fatalf("expected artifact_not_found failure, got %s", result)
	}
}
