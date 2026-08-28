package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/data"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/service"
)

// dbTimeout 是单次数据库查询的最大等待时间，防止慢 DB 永久阻塞 goroutine（BUG2 修复）。
const dbTimeout = 10 * time.Second

// dbContext 返回一个带固定超时、与父 ctx 取消无关的衍生 context，
// 用于 Manager 内部的 DB 查询。使用 context.WithoutCancel 防止请求 cancel
// 传播到 DB 写操作（如 UpdateLastSeen）。
func dbContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(parent)
	return context.WithTimeout(base, dbTimeout)
}

const sessionStopTimeout = 10 * time.Second

type Manager struct {
	cacheRoot      string
	fileService    *service.FileService
	sourceService  *service.SourceService
	sessionRepo    repository.SessionRepository
	sessions       map[string]*Session
	mu             sync.Mutex
	fullDeleteFunc func(ctx context.Context, sessionID string) error
	cleanupStop    chan struct{}
}

func NewManager(cacheRoot string, fileService *service.FileService, sourceService *service.SourceService) *Manager {
	return &Manager{
		cacheRoot:     cacheRoot,
		fileService:   fileService,
		sourceService: sourceService,
		sessions:      make(map[string]*Session),
	}
}

func (m *Manager) SetSessionRepository(repo repository.SessionRepository) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionRepo = repo
}

// SetFullDeleteFunc configures the complete deletion operation used by TTL cleanup.
func (m *Manager) SetFullDeleteFunc(fn func(ctx context.Context, sessionID string) error) {
	if fn == nil {
		panic("session full-delete function must not be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fullDeleteFunc = fn
}

// GetOrCreate 查找或创建 session。
// ctx 用于限制 DB 查询时长（BUG2 修复），不传播取消语义到写操作。
// DB IO 在 Manager 锁外执行：锁内只做 map 快照与写入，避免慢查询阻塞所有会话操作。
func (m *Manager) GetOrCreate(ctx context.Context, sessionID, workspaceID, userID string) (*Session, bool, error) {
	m.mu.Lock()
	repo := m.sessionRepo
	var cached *Session
	if sessionID != "" {
		cached = m.sessions[sessionID]
	}
	m.mu.Unlock()

	if cached != nil {
		if cached.WorkspaceID != workspaceID || cached.UserID != userID {
			return nil, false, fmt.Errorf("not authorized to access this session")
		}
		cached.Touch()
		if repo != nil {
			wCtx, cancel := dbContext(ctx)
			err := repo.UpdateLastSeen(wCtx, sessionID)
			cancel()
			if err != nil {
				return nil, false, fmt.Errorf("failed to update session last-seen time: %w", err)
			}
		}
		return cached, false, nil
	}

	id := sessionID
	if id == "" {
		id = "s_" + uuid.New().String()[:8]
	}

	created := true
	if sessionID != "" && repo != nil {
		qCtx, cancel := dbContext(ctx)
		record, err := repo.GetByID(qCtx, sessionID)
		cancel()
		if err == nil {
			if record.WorkspaceID != workspaceID || record.UserID != userID {
				return nil, false, fmt.Errorf("not authorized to access this session")
			}
			workspaceID = record.WorkspaceID
			userID = record.UserID
			created = false
		} else if !errors.Is(err, repository.ErrNotFound) {
			return nil, false, err
		}
	}

	m.mu.Lock()
	if sess, ok := m.sessions[id]; ok {
		m.mu.Unlock()
		if sess.WorkspaceID != workspaceID || sess.UserID != userID {
			return nil, false, fmt.Errorf("not authorized to access this session")
		}
		sess.Touch()
		return sess, false, nil
	}
	sess, err := New(id, workspaceID, userID, m.cacheRoot, m.fileService, m.sourceService)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	m.sessions[id] = sess
	m.mu.Unlock()

	if created && repo != nil {
		now := time.Now()
		wCtx, cancel := dbContext(ctx)
		err := repo.Create(wCtx, &domain.Session{
			ID:          id,
			WorkspaceID: workspaceID,
			UserID:      userID,
			Title:       "",
			Status:      domain.SessionStatusActive,
			CreatedAt:   now,
			UpdatedAt:   now,
			LastSeenAt:  now,
		})
		cancel()
		if err != nil {
			m.mu.Lock()
			if m.sessions[id] == sess {
				delete(m.sessions, id)
			}
			m.mu.Unlock()
			return nil, false, err
		}
	}
	if repo != nil && !created {
		wCtx, cancel := dbContext(ctx)
		err := repo.UpdateLastSeen(wCtx, id)
		cancel()
		if err != nil {
			return nil, false, fmt.Errorf("failed to update session last-seen time: %w", err)
		}
	}
	return sess, created, nil
}

// Get returns an authorized live session object. Persistent runtime state is
// restored by the handler before the reconstructed session is used for a run.
func (m *Manager) Get(ctx context.Context, sessionID, workspaceID, userID string) (*Session, error) {
	m.mu.Lock()
	repo := m.sessionRepo
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()

	if !ok {
		if repo == nil {
			return nil, fmt.Errorf("session does not exist: %s", sessionID)
		}
		qCtx, cancel := dbContext(ctx)
		record, err := repo.GetByID(qCtx, sessionID)
		cancel()
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return nil, fmt.Errorf("session does not exist: %s", sessionID)
			}
			return nil, fmt.Errorf("failed to read session %s: %w", sessionID, err)
		}
		if record.WorkspaceID != workspaceID || record.UserID != userID {
			return nil, fmt.Errorf("not authorized to access this session")
		}

		m.mu.Lock()
		if existing, ok := m.sessions[sessionID]; ok {
			sess = existing
		} else {
			sess, err = New(record.ID, record.WorkspaceID, record.UserID, m.cacheRoot, m.fileService, m.sourceService)
			if err != nil {
				m.mu.Unlock()
				return nil, err
			}
			log.Printf("session %s reconstructed from persistent identity workspace=%s", sessionID, workspaceID)
			m.sessions[sessionID] = sess
		}
		m.mu.Unlock()
	}

	if sess.WorkspaceID != workspaceID || sess.UserID != userID {
		return nil, fmt.Errorf("not authorized to access this session")
	}
	sess.Touch()
	if repo != nil {
		wCtx, cancel := dbContext(ctx)
		err := repo.UpdateLastSeen(wCtx, sessionID)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("failed to update session last-seen time: %w", err)
		}
	}
	return sess, nil
}

func (m *Manager) Peek(sessionID, workspaceID, userID string) (*Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		return nil, false, nil
	}
	if sess.WorkspaceID != workspaceID || sess.UserID != userID {
		return nil, false, fmt.Errorf("not authorized to access this session")
	}
	return sess, true, nil
}

func (m *Manager) Delete(sessionID, workspaceID, userID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	if ok {
		if sess.WorkspaceID != workspaceID || sess.UserID != userID {
			m.mu.Unlock()
			return fmt.Errorf("not authorized to access this session")
		}
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if ok {
		return sess.Destroy()
	}
	return data.DestroySessionDB(m.cacheRoot, sessionID)
}

func (m *Manager) Stop(sessionID, workspaceID, userID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	if sess.WorkspaceID != workspaceID || sess.UserID != userID {
		return fmt.Errorf("not authorized to access this session")
	}
	sess.CancelRun("")
	if !sess.WaitUntilIdle(sessionStopTimeout) {
		return fmt.Errorf("session still has running tasks, cannot delete")
	}
	return nil
}

// IsSessionLive 判断 session 是否存在于内存中（有活跃引擎 / 等待态 run）。
// 用于 bootstrap 阶段识别 stale run（DB 中仍为 running/waiting_user_input 但无引擎持有）。
func (m *Manager) IsSessionLive(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[sessionID]
	return ok
}
