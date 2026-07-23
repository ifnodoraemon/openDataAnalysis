package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type mockWorkspaceRepo struct {
	isMember bool
	err      error
}

func (m *mockWorkspaceRepo) GetByID(ctx context.Context, id string) (*domain.Workspace, error) {
	return nil, nil
}
func (m *mockWorkspaceRepo) ListByUser(ctx context.Context, userID string) ([]domain.Workspace, error) {
	return nil, nil
}
func (m *mockWorkspaceRepo) IsMember(ctx context.Context, workspaceID, userID string) (bool, error) {
	return m.isMember, m.err
}
func (m *mockWorkspaceRepo) CreateWorkspace(ctx context.Context, ws *domain.Workspace) error {
	return nil
}
func (m *mockWorkspaceRepo) AddMember(ctx context.Context, member *domain.WorkspaceMember) error {
	return nil
}

func TestRoleWeightAndHierarchy(t *testing.T) {
	if !HasMinRole(domain.WorkspaceRoleOwner, domain.WorkspaceRoleAdmin) {
		t.Fatal("owner should satisfy admin requirement")
	}
	if !HasMinRole(domain.WorkspaceRoleAdmin, domain.WorkspaceRoleMember) {
		t.Fatal("admin should satisfy member requirement")
	}
	if HasMinRole(domain.WorkspaceRoleMember, domain.WorkspaceRoleAdmin) {
		t.Fatal("member should not satisfy admin requirement")
	}
}

func TestRequireWorkspaceRoleMiddleware(t *testing.T) {
	repo := &mockWorkspaceRepo{isMember: true}
	mw := RequireWorkspaceRole(repo, domain.WorkspaceRoleMember)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test missing identity
	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on missing identity, got %d", rr.Code)
	}

	// Test authorized member
	identity := Identity{UserID: "u1", WorkspaceID: "w1"}
	req = httptest.NewRequest("GET", "/", nil).WithContext(WithIdentity(context.Background(), identity))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for member, got %d", rr.Code)
	}
}
