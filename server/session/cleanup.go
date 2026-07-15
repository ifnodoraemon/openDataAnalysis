package session

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CleanupExpiredSessions 清理超过 TTL 的空闲 session。
// 在真正删除前会在锁内二次验证状态，避免扫描与删除之间的竞态。
// 如果 Manager.FullDeleteFunc 已设置，则通过全链路路径清理（包含文件和存储对象）；
// 否则退化到 Session.Destroy + repo.Delete。
func (m *Manager) CleanupExpiredSessions(ttlHours int) int {
	if ttlHours <= 0 {
		return 0
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
	for _, id := range candidates {
		// 第二步：在锁内再次验证状态（防止扫描期间会话被激活）
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
			// 注意：此处不立即 delete，待清理操作成功后再从 map 中移除，
			// 避免 FullDeleteFunc 失败时内存已清、DB 尚存导致数据不一致。
		}
		m.mu.Unlock()

		if !ok {
			continue
		}

		// 第三步：执行清理（成功后才从 map 中删除）
		var cleanErr error
		if m.FullDeleteFunc != nil {
			// 全链路删除：停止运行 + 删除文件记录 + 删除存储对象 + 删除数据库行
			cleanErr = m.FullDeleteFunc(context.Background(), id)
			if cleanErr != nil {
				log.Printf("cleanup session %s full-delete error: %v", id, cleanErr)
			}
		} else {
			// 退化路径：销毁本地 SQLite 缓存
			cleanErr = sess.Destroy()
			if cleanErr != nil {
				log.Printf("cleanup session %s destroy error: %v", id, cleanErr)
			} else if m.sessionRepo != nil {
				_ = m.sessionRepo.Delete(context.Background(), id)
			}
		}
		if cleanErr != nil {
			continue
		}
		// 清理成功后才从内存 map 中移除，保证内存与 DB 一致性
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		cleaned++
	}

	// 第四步：清理 DB 独占的过期会话（内存中未加载的会话）
	if m.sessionRepo != nil && m.FullDeleteFunc != nil {
		dbExpired, err := m.sessionRepo.ListExpired(context.Background(), cutoff, 100)
		if err == nil {
			for _, sess := range dbExpired {
				m.mu.Lock()
				_, inMem := m.sessions[sess.ID]
				m.mu.Unlock()
				if !inMem {
					if err := m.FullDeleteFunc(context.Background(), sess.ID); err != nil {
						log.Printf("cleanup: db-only session %s full-delete error: %v", sess.ID, err)
					} else {
						cleaned++
					}
				}
			}
		} else {
			log.Printf("cleanup: list expired sessions error: %v", err)
		}
	}

	return cleaned
}

// CleanupOldTraces 清理超过保留天数的 LLM debug trace 目录
func CleanupOldTraces(traceDir string, retentionDays int) int {
	if retentionDays <= 0 || traceDir == "" {
		return 0
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		return 0
	}

	cleaned := 0
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
				log.Printf("cleanup trace dir %s: %v", fullPath, err)
			} else {
				cleaned++
			}
		}
	}
	return cleaned
}

// CleanupTempDir 清理临时目录中的所有文件
func CleanupTempDir(tempDir string) error {
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
		_ = os.RemoveAll(filepath.Join(tempDir, entry.Name()))
	}
	return nil
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
	periodicEnabled := sessionTTLHours > 0 || traceRetentionDays > 0 || tempDir != ""
	if !periodicEnabled && !tempCleanupOnStart {
		return
	}

	if tempCleanupOnStart && tempDir != "" {
		if entries, err := os.ReadDir(tempDir); err == nil && len(entries) > 0 {
			log.Printf("cleanup: clearing %d temp entries on start", len(entries))
			_ = CleanupTempDir(tempDir)
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
				if n := m.CleanupExpiredSessions(sessionTTLHours); n > 0 {
					log.Printf("cleanup: removed %d expired sessions", n)
				}
				if n := CleanupOldTraces(traceDir, traceRetentionDays); n > 0 {
					log.Printf("cleanup: removed %d old trace directories", n)
				}
				if tempDir != "" {
					cleanupStaleTemp(tempDir, 4*time.Hour)
				}
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
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(tempDir, entry.Name()))
		}
	}
}
