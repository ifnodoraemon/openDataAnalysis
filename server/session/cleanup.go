package session

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CleanupExpiredSessions removes idle sessions through the configured complete
// deletion boundary. It never substitutes a partial local-cache deletion.
func (m *Manager) CleanupExpiredSessions(ttlHours int) (int, error) {
	if ttlHours <= 0 {
		return 0, nil
	}
	m.mu.Lock()
	deleteSession := m.fullDeleteFunc
	m.mu.Unlock()
	if deleteSession == nil {
		return 0, fmt.Errorf("session TTL cleanup requires a complete deletion function")
	}

	cutoff := time.Now().Add(-time.Duration(ttlHours) * time.Hour)

	// 第一步：在锁内收集候选 ID
	m.mu.Lock()
	var candidates []string
	for id, sess := range m.sessions {
		sess.mu.Lock()
		isIdle := sess.ActiveRun == nil || sess.ActiveRun.Status != "running"
		lastSeen := sess.LastSeenAt
		sess.mu.Unlock()
		if isIdle && lastSeen.Before(cutoff) {
			candidates = append(candidates, id)
		}
	}
	m.mu.Unlock()

	cleaned := 0
	var resultErr error
	for _, id := range candidates {
		// 第二步：在锁内再次验证状态并原子摘除（防止校验通过后 run 重新启动）。
		// 删除本身的 IO 在锁外执行，校验与 map 移除必须在同一临界区内完成。
		m.mu.Lock()
		sess, ok := m.sessions[id]
		if ok {
			sess.mu.Lock()
			isStillIdle := sess.ActiveRun == nil || sess.ActiveRun.Status != "running"
			stillExpired := sess.LastSeenAt.Before(cutoff)
			sess.mu.Unlock()
			if !isStillIdle || !stillExpired {
				// 状态已改变，跳过该会话
				m.mu.Unlock()
				continue
			}
			delete(m.sessions, id)
		}
		m.mu.Unlock()

		if !ok {
			continue
		}

		// 已摘除但尚未关闭的会话：在锁外取消其残留 run，避免删除期间继续写入
		if sess != nil {
			sess.CancelRun("")
		}

		cleanErr := deleteSession(context.Background(), id)
		if cleanErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("delete expired session %s: %w", id, cleanErr))
			// 删除失败时回填内存对象，保持内存与 DB 一致（若期间已被重建则跳过）
			m.mu.Lock()
			if _, exists := m.sessions[id]; !exists {
				m.sessions[id] = sess
			}
			m.mu.Unlock()
			continue
		}
		cleaned++
	}

	// 第四步：清理 DB 独占的过期会话（内存中未加载的会话）
	if m.sessionRepo != nil {
		dbExpired, err := m.sessionRepo.ListExpired(context.Background(), cutoff, 100)
		if err == nil {
			for _, sess := range dbExpired {
				m.mu.Lock()
				_, inMem := m.sessions[sess.ID]
				m.mu.Unlock()
				if !inMem {
					if err := deleteSession(context.Background(), sess.ID); err != nil {
						resultErr = errors.Join(resultErr, fmt.Errorf("delete expired persisted session %s: %w", sess.ID, err))
					} else {
						cleaned++
					}
				}
			}
		} else {
			resultErr = errors.Join(resultErr, fmt.Errorf("list expired persisted sessions: %w", err))
		}
	}

	return cleaned, resultErr
}

// CleanupOldTraces 清理超过保留天数的 LLM debug trace 目录
func CleanupOldTraces(traceDir string, retentionDays int) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	if traceDir == "" {
		return 0, fmt.Errorf("trace retention requires a trace directory")
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		return 0, fmt.Errorf("read trace directory: %w", err)
	}

	cleaned := 0
	var resultErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// trace 目录名格式: YYYY-MM-DD
		name := entry.Name()
		if len(name) != 10 || !isDateDirName(name) {
			continue
		}
		parsed, err := time.Parse("2006-01-02", name)
		if err != nil {
			continue
		}
		if parsed.Before(cutoff) {
			fullPath := filepath.Join(traceDir, name)
			if err := os.RemoveAll(fullPath); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove trace directory %s: %w", fullPath, err))
			} else {
				cleaned++
			}
		}
	}
	return cleaned, resultErr
}

// CleanupTempDir 清理临时目录中的所有文件
func CleanupTempDir(tempDir string) (resultErr error) {
	if tempDir == "" {
		return nil
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(tempDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove temporary path %s: %w", path, err))
		}
	}
	return resultErr
}

func isDateDirName(name string) bool {
	// 快速检查 YYYY-MM-DD 格式
	for i, ch := range name {
		if i == 4 || i == 7 {
			if ch != '-' {
				return false
			}
		} else if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// StartPeriodicCleanup 启动后台清理协程
func (m *Manager) StartPeriodicCleanup(sessionTTLHours, traceRetentionDays int, traceDir, tempDir string, tempCleanupOnStart bool) {
	if sessionTTLHours > 0 {
		m.mu.Lock()
		deleteSession := m.fullDeleteFunc
		m.mu.Unlock()
		if deleteSession == nil {
			panic("session TTL cleanup requires a complete deletion function")
		}
	}
	if traceRetentionDays > 0 && traceDir == "" {
		panic("trace retention requires a trace directory")
	}
	periodicEnabled := sessionTTLHours > 0 || traceRetentionDays > 0 || tempDir != ""
	if !periodicEnabled && !tempCleanupOnStart {
		return
	}

	if tempCleanupOnStart && tempDir != "" {
		if entries, err := os.ReadDir(tempDir); err != nil && !os.IsNotExist(err) {
			log.Printf("cleanup: inspect temp directory on start: %v", err)
		} else if len(entries) > 0 {
			log.Printf("cleanup: clearing %d temp entries on start", len(entries))
			if err := CleanupTempDir(tempDir); err != nil {
				log.Printf("cleanup: clear temp directory on start: %v", err)
			}
		}
	}

	if !periodicEnabled {
		return
	}

	m.mu.Lock()
	if m.cleanupStop != nil {
		m.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	m.cleanupStop = stop
	m.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVER] periodic session cleanup: %v", r)
			}
		}()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				log.Println("cleanup: periodic cleanup stopped")
				return
			case <-ticker.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[PANIC RECOVER] periodic session cleanup tick: %v", r)
						}
					}()
					if n, err := m.CleanupExpiredSessions(sessionTTLHours); err != nil {
						log.Printf("cleanup: expired session cleanup failed: %v", err)
					} else if n > 0 {
						log.Printf("cleanup: removed %d expired sessions", n)
					}
					if n, err := CleanupOldTraces(traceDir, traceRetentionDays); err != nil {
						log.Printf("cleanup: trace cleanup failed: %v", err)
					} else if n > 0 {
						log.Printf("cleanup: removed %d old trace directories", n)
					}
					if tempDir != "" {
						cleanupStaleTemp(tempDir, 4*time.Hour)
					}
				}()
			}
		}
	}()

	var parts []string
	if sessionTTLHours > 0 {
		parts = append(parts, "session_ttl="+strconv.Itoa(sessionTTLHours)+"h")
	}
	if traceRetentionDays > 0 {
		parts = append(parts, "trace_retention="+strconv.Itoa(traceRetentionDays)+"d")
	}
	log.Printf("cleanup: periodic cleanup started (%s)", strings.Join(parts, ", "))
}

// StopPeriodicCleanup 停止后台清理协程
func (m *Manager) StopPeriodicCleanup() {
	m.mu.Lock()
	stop := m.cleanupStop
	m.cleanupStop = nil
	m.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func cleanupStaleTemp(tempDir string, maxAge time.Duration) {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("cleanup: inspect stale temp entries: %v", err)
		}
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			log.Printf("cleanup: inspect temp entry %s: %v", entry.Name(), err)
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(tempDir, entry.Name())
			if err := os.RemoveAll(path); err != nil {
				log.Printf("cleanup: remove stale temp path %s: %v", path, err)
			}
		}
	}
}
