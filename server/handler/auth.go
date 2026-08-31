package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"golang.org/x/time/rate"
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

	authIPRatePerMinute  = 10
	authIPRateBurst      = 10
	loginIPLimiterMap    = NewRateLimiterMap(rate.Limit(float64(authIPRatePerMinute)/60.0), authIPRateBurst)
	registerIPLimiterMap = NewRateLimiterMap(rate.Limit(float64(authIPRatePerMinute)/60.0), authIPRateBurst)
)

func checkIPRateLimit(limiterMap *RateLimiterMap, r *http.Request) bool {
	return limiterMap.getLimiter(getClientIP(r)).Allow()
}

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
	if !checkIPRateLimit(loginIPLimiterMap, r) {
		http.Error(w, "请求过于频繁，请稍后重试", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req loginRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		http.Error(w, "邮箱和密码不能为空", http.StatusBadRequest)
		return
	}

	if !checkLoginRate(req.Email) {
		http.Error(w, "登录尝试过于频繁，请稍后重试", http.StatusTooManyRequests)
		return
	}

	user, err := userRepo.GetByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Pay the bcrypt cost on the unknown-email path too, so response
			// latency does not reveal whether the account exists.
			if dummyHash := auth.DummyPasswordHash(); dummyHash != "" {
				auth.VerifyPassword(req.Password, dummyHash)
			}
			recordLoginAttempt(req.Email)
			http.Error(w, "邮箱或密码错误", http.StatusUnauthorized)
			return
		}
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}

	if !auth.VerifyPassword(req.Password, user.PasswordHash) {
		recordLoginAttempt(req.Email)
		http.Error(w, "邮箱或密码错误", http.StatusUnauthorized)
		return
	}

	workspaces, err := workspaceRepo.ListByUser(r.Context(), user.ID)
	if err != nil || len(workspaces) == 0 {
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
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
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
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

	writeJSON(w, http.StatusOK, resp)
}

func authCookieSecure() bool {
	return config.Cfg == nil || config.Cfg.AuthCookieSecure
}

func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "oda_token",
		Value:    token,
		Path:     "/",
		MaxAge:   604800,
		HttpOnly: true,
		Secure:   authCookieSecure(),
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
		Secure:   authCookieSecure(),
		SameSite: http.SameSiteStrictMode,
	})
}

func bearerTokenFromRequest(r *http.Request) string {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}

func LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if tokenManager != nil {
		tokens := make([]string, 0, 2)
		if bearer := bearerTokenFromRequest(r); bearer != "" {
			tokens = append(tokens, bearer)
		}
		if cookie, err := r.Cookie("oda_token"); err == nil && cookie.Value != "" {
			tokens = append(tokens, cookie.Value)
		}
		for _, token := range tokens {
			if err := tokenManager.Revoke(token); err != nil {
				log.Printf("logout token revocation skipped: %v", err)
			}
		}
	}
	clearAuthCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func selectWorkspace(workspaces []domain.Workspace, workspaceID string) (domain.Workspace, error) {
	if workspaceID != "" {
		for _, workspace := range workspaces {
			if workspace.ID == workspaceID {
				return workspace, nil
			}
		}
		return domain.Workspace{}, fmt.Errorf("指定的工作空间不存在或无权访问")
	}
	if len(workspaces) != 1 {
		return domain.Workspace{}, fmt.Errorf("账户属于多个工作空间时必须提供 workspaceId")
	}
	return workspaces[0], nil
}

func SwitchWorkspaceHandler(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())

	var req switchWorkspaceRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		http.Error(w, "workspaceId 不能为空", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.WorkspaceID) != req.WorkspaceID {
		http.Error(w, "workspaceId 必须保持原值", http.StatusBadRequest)
		return
	}

	ok, err := workspaceRepo.IsMember(r.Context(), req.WorkspaceID, identity.UserID)
	if err != nil {
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "无权访问此工作空间", http.StatusForbidden)
		return
	}

	workspace, err := workspaceRepo.GetByID(r.Context(), req.WorkspaceID)
	if err != nil {
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
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
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
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
	writeJSON(w, http.StatusOK, resp)
}

type registerRequest struct {
	Name          string `json:"name"`
	Email         string `json:"email"`
	Password      string `json:"password"`
	WorkspaceName string `json:"workspaceName,omitempty"`
}

func RegisterHandler(w http.ResponseWriter, r *http.Request) {
	if !checkIPRateLimit(registerIPLimiterMap, r) {
		http.Error(w, "请求过于频繁，请稍后重试", http.StatusTooManyRequests)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req registerRequest
	if err := decodeRequestJSON(r, &req); err != nil {
		http.Error(w, "请求格式无效", http.StatusBadRequest)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	if req.Email == "" || strings.TrimSpace(req.Password) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.WorkspaceName) == "" {
		http.Error(w, "姓名、邮箱、密码和工作空间名称不能为空", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) != req.Name || strings.TrimSpace(req.WorkspaceName) != req.WorkspaceName {
		http.Error(w, "姓名和工作空间名称必须保持原值", http.StatusBadRequest)
		return
	}

	if err := auth.ValidatePasswordStrength(req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	existing, err := userRepo.GetByEmail(r.Context(), req.Email)
	if err == nil && existing != nil {
		http.Error(w, "该邮箱已注册", http.StatusConflict)
		return
	}
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		http.Error(w, "检查现有用户失败", http.StatusInternalServerError)
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "处理密码失败", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	userID := "usr_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	workspaceID := "ws_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	workspaceName := req.WorkspaceName

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
		http.Error(w, "创建用户失败", http.StatusInternalServerError)
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
		http.Error(w, "创建工作空间失败", http.StatusInternalServerError)
		return
	}

	member := &domain.WorkspaceMember{
		WorkspaceID: workspaceID,
		UserID:      userID,
		Role:        domain.WorkspaceRoleOwner,
		CreatedAt:   now,
	}
	if err := workspaceRepo.AddMember(r.Context(), member); err != nil {
		http.Error(w, "创建工作空间成员关系失败", http.StatusInternalServerError)
		return
	}

	identity := auth.Identity{
		UserID:      userID,
		UserName:    req.Name,
		UserEmail:   req.Email,
		WorkspaceID: workspaceID,
		Workspace:   workspaceName,
	}
	token, err := tokenManager.Sign(identity, 7*24*time.Hour)
	if err != nil {
		http.Error(w, "生成登录凭证失败", http.StatusInternalServerError)
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
		"workspaces": []map[string]string{{
			"id":   workspace.ID,
			"name": workspace.Name,
		}},
	}

	writeJSON(w, http.StatusCreated, resp)
}
