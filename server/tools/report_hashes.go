package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// BlockContentHashes returns one content hash per block, covering everything that
// can change that block's pixels: its own fields, the chart options it references,
// and the report layout.
//
// This is what makes dirty-block capture reuse work. Hashing the report version
// instead would invalidate every slice on every mutation, which is the same as
// having no cache at all.
func (s *ReportState) BlockContentHashes() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	hashes := make(map[string]string, len(s.Blocks))
	if len(s.Blocks) == 0 {
		return hashes
	}

	charts := make(map[string]json.RawMessage, len(s.Charts))
	for _, chart := range s.Charts {
		charts[chart.ID] = chart.Option
	}
	// Layout applies to the whole document, so it belongs to every block's hash.
	layoutDigest := digestOf(struct {
		CustomCSS string
		BodyClass string
	}{s.Layout.CustomCSS, s.Layout.BodyClass})

	for _, block := range s.Blocks {
		chartIDs := make([]string, 0, 1)
		if block.ChartID != "" {
			chartIDs = append(chartIDs, block.ChartID)
		}
		chartIDs = append(chartIDs, ChartReferences(block.Content)...)
		sort.Strings(chartIDs)

		referenced := make([]json.RawMessage, 0, len(chartIDs))
		for _, chartID := range chartIDs {
			referenced = append(referenced, charts[chartID])
		}
		hashes[block.ID] = digestOf(struct {
			ID      string
			Kind    string
			Title   string
			Content string
			ChartID string
			Sources []EvidenceRef
			Charts  []json.RawMessage
			Layout  string
		}{block.ID, block.Kind, block.Title, block.Content, block.ChartID, block.Sources, referenced, layoutDigest})
	}
	return hashes
}

func digestOf(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])[:32]
}
