package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

// Large tool results are spilled to disk instead of being truncated in place.
// The conversation keeps a bounded preview plus an id, so the omitted middle is
// still recoverable instead of being lost.
const (
	// maxSpilledResultBytes bounds what may be written for one result. A larger
	// payload is kept as a bounded preview only.
	maxSpilledResultBytes = 8 << 20
	// spillIDBytes is the length of the content-addressed id.
	spillIDBytes = 16
)

// toolResultSpillDir is where spilled results live. The directory sits beside
// the other runtime scratch state so a deployment's cleanup covers it.
func toolResultSpillDir() string {
	base := "./data/tmp"
	if config.Cfg != nil && config.Cfg.TempDir != "" {
		base = config.Cfg.TempDir
	}
	return filepath.Join(base, "tool-results")
}

// spillToolResult stores a result and returns its content-addressed id.
//
// The id is derived from the content, so spilling the same result twice (a live
// run and a later history rebuild) resolves to the same file and the locator a
// conversation carries stays valid across restarts.
func spillToolResult(content string) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("nothing to spill")
	}
	if len(content) > maxSpilledResultBytes {
		return "", fmt.Errorf("result exceeds the %d byte spill limit", maxSpilledResultBytes)
	}
	sum := sha256.Sum256([]byte(content))
	id := hex.EncodeToString(sum[:spillIDBytes])

	dir := toolResultSpillDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create spill directory: %w", err)
	}
	path := filepath.Join(dir, id+".json")
	if _, statErr := os.Stat(path); statErr == nil {
		return id, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write spilled result: %w", err)
	}
	return id, nil
}

// readSpilledToolResult returns a rune-bounded window of a spilled result.
func readSpilledToolResult(id string, offset, limit int) (string, int, error) {
	if !validSpillID(id) {
		return "", 0, fmt.Errorf("result_id must be a %d-character lowercase hex value", spillIDBytes*2)
	}
	raw, err := os.ReadFile(filepath.Join(toolResultSpillDir(), id+".json"))
	if err != nil {
		return "", 0, fmt.Errorf("spilled result %s is not available: %w", id, err)
	}
	runes := []rune(string(raw))
	if offset < 0 {
		return "", 0, fmt.Errorf("offset must not be negative")
	}
	if offset > len(runes) {
		offset = len(runes)
	}
	if limit <= 0 || limit > 6000 {
		limit = 6000
	}
	end := offset + limit
	if end > len(runes) {
		end = len(runes)
	}
	return string(runes[offset:end]), len(runes), nil
}

// validSpillID keeps an id from addressing anything outside the spill
// directory: it must be exactly the hex digest length, so path separators and
// traversal sequences cannot appear.
func validSpillID(id string) bool {
	if len(id) != spillIDBytes*2 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
