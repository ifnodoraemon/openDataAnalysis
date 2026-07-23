package handler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
)

type loginRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	WorkspaceID string `json:"workspaceId,omitempty"`
}

type switchWorkspaceRequest struct {
	WorkspaceID string `json:"workspaceId"`
}

var (
	loginRateMu      sync.Mutex
	loginAttempts    = make(map[string][]time.Time)
	loginRateLimit   = 5
	loginRateWindow  = 5 * time.Minute
	loginCleanupStop = make(chan struct{})
)

func init() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVER] login rate limit cleanup ticker: %v", r)
			}
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanupLoginAttempts()
			case <-loginCleanupStop:
				return
			}
		}
	}()
}

func StopLoginCleanup() {
	close(loginCleanupStop)
}

func cleanupLoginAttempts() {
	loginRateMu.Lock()
	defer loginRateMu.Unlock()
	cutoff := time.Now().Add(-loginRateWindow)
	for email, attempts := range loginAttempts {
		valid := make([]time.Time, 0, len(attempts))
		for _, t := range attempts {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(loginAttempts, email)
		} else {
			loginAttempts[email] = valid
		}
	}
}

func checkLoginRate(email string) bool {
	loginRateMu.Lock()
	defer loginRateMu.Unlock()
	now := time.Now()
	cutoff := now.Add(-loginRateWindow)
	attempts := loginAttempts[email]
	valid := make([]time.Time, 0, len(attempts))
	for _, t := range attempts {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	loginAttempts[email] = valid
	return len(valid) < loginRateLimit
}

func recordLoginAttempt(email string) {
	loginRateMu.Lock()
	defer loginRateMu.Unlock()
	loginAttempts[email] = append(loginAttempts[email], time.Now())
}

func LoginHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request format", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		http.Error(w, "email and password cannot be empty", http.StatusBadRequest)
		return
	}

	if !checkLoginRate(req.Email) {
		http.Error(w, "too many login attempts, please try again later", http.StatusTooManyRequests)
		return
	}

	user, err := userRepo.GetByEmail(r.Context(), req.Email)
	if err != nil {
		if err == sql.ErrNoRows {
			recordLoginAttempt(req.Email)
			http.Error(w, "invalid email or password", http.StatusUnauthorized)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if !auth.VerifyPassword(req.Password, user.PasswordHash) {
		recordLoginAttempt(req.Email)
		http.Error(w, "invalid email or password", http.StatusUnauthorized)
		return
	}

	workspaces, err := workspaceRepo.ListByUser(r.Context(), user.ID)
	if err != nil || len(workspaces) == 0 {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	activeWorkspace, err := selectWorkspace(workspaces, req.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	identity := auth.Identity{
		UserID:      user.ID,
		UserName:    user.Name,
		UserEmail:   user.Email,
		WorkspaceID: activeWorkspace.ID,
		Workspace:   activeWorkspace.Name,
	}
	token, err := tokenManager.Sign(identity, 7*24*time.Hour)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	responseWorkspaces := make([]map[string]string, 0, len(workspaces))
	for _, workspace := range workspaces {
		responseWorkspaces = append(responseWorkspaces, map[string]string{
			"id":   workspace.ID,
			"name": workspace.Name,
		})
	}

	setAuthCookie(w, token)

	resp := map[string]interface{}{
		"token": token,
		"user": map[string]string{
			"id":    user.ID,
			"name":  user.Name,
			"email": user.Email,
		},
		"workspace": map[string]string{
			"id":   activeWorkspace.ID,
			"name": activeWorkspace.Name,
		},
		"workspaces": responseWorkspaces,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "oda_token",
		Value:    token,
		Path:     "/",
		MaxAge:   604800,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "oda_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	clearAuthCookie(w)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func selectWorkspace(workspaces []domain.Workspace, workspaceID string) (domain.Workspace, error) {
	if workspaceID != "" {
		for _, workspace := range workspaces {
			if workspace.ID == workspaceID {
				return workspace, nil
			}
		}
		return domain.Workspace{}, fmt.Errorf("specified workspace does not exist or not authorized")
	}
	return workspaces[0], nil
}

func SwitchWorkspaceHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())

	var req switchWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request format", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		http.Error(w, "workspaceId cannot be empty", http.StatusBadRequest)
		return
	}

	ok, err := workspaceRepo.IsMember(r.Context(), req.WorkspaceID, identity.UserID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "not authorized to access this workspace", http.StatusForbidden)
		return
	}

	workspace, err := workspaceRepo.GetByID(r.Context(), req.WorkspaceID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	newIdentity := auth.Identity{
		UserID:      identity.UserID,
		UserName:    identity.UserName,
		UserEmail:   identity.UserEmail,
		WorkspaceID: workspace.ID,
		Workspace:   workspace.Name,
	}
	token, err := tokenManager.Sign(newIdentity, 7*24*time.Hour)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	setAuthCookie(w, token)

	resp := map[string]interface{}{
		"token": token,
		"workspace": map[string]string{
			"id":   workspace.ID,
			"name": workspace.Name,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type registerRequest struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Password      string `json:"password"`
	WorkspaceName string `json:"workspaceName,omitempty"`
}

func RegisterHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request format", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	req.Password = strings.TrimSpace(req.Password)

	if req.Email == "" || req.Password == "" || req.Name == "" {
		http.Error(w, "name, email, and password cannot be empty", http.StatusBadRequest)
		return
	}

	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	existing, err := userRepo.GetByEmail(r.Context(), req.Email)
	if err == nil && existing != nil {
		http.Error(w, "email already registered", http.StatusConflict)
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "failed to process password", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	userID := "usr_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	workspaceID := "ws_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	workspaceName := strings.TrimSpace(req.WorkspaceName)
	if workspaceName == "" {
		workspaceName = req.Name + "'s Workspace"
	}

	user := &domain.User{
		ID:           userID,
		Email:        req.Email,
		PasswordHash: passwordHash,
		Name:         req.Name,
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := userRepo.Create(r.Context(), user); err != nil {
		http.Error(w, "failed to create user", http.StatusInternalServerError)
		return
	}

	workspace := &domain.Workspace{
		ID:          workspaceID,
		Name:        workspaceName,
		Slug:        workspaceID,
		OwnerUserID: userID,
		Status:      domain.WorkspaceStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := workspaceRepo.CreateWorkspace(r.Context(), workspace); err != nil {
		http.Error(w, "failed to create workspace", http.StatusInternalServerError)
		return
	}

	member := &domain.WorkspaceMember{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        domain.WorkspaceRoleOwner,
		CreatedAt:   now,
	}
	_ = workspaceRepo.AddMember(r.Context(), member)

	identity := auth.Identity{
		UserID:      userID,
		UserName:    req.Name,
		UserEmail:   req.Email,
		WorkspaceID: workspaceID,
		Workspace:   workspaceName,
	}
	token, err := tokenManager.Sign(identity, 7*24*time.Hour)
	if err != nil {
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	setAuthCookie(w, token)

	resp := map[string]interface{}{
		"token": token,
		"user": map[string]string{
			"id":    user.ID,
			"name":  user.Name,
			"email": user.Email,
		},
		"workspace": map[string]string{
			"id":   workspace.ID,
			"name": workspace.Name,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}
