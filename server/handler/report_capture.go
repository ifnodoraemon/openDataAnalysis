package handler

import (
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// triggerReportCapture schedules a background capture of the current report.
//
// It never blocks: the engine reads the newest ready view on its next request,
// and a missing view simply means this turn carries facts only. The coordinator
// owns dedupe and single-flight, so calling this on every report mutation is the
// intended usage rather than a bug.
func triggerReportCapture(sess *session.Session, reason string) {
	if sess == nil || sess.ReportState == nil {
		return
	}
	coordinator := sess.ReportViewCoordinator()
	if coordinator == nil {
		return
	}

	sess.ReportState.RLock()
	version := int(sess.ReportState.MutationVersion)
	// Capture the same bytes the user sees: the sanitized document, not the raw
	// render. Anything else would have the model review a page nobody can open.
	html := tools.RenderSafeReportHTML(sess.ReportState.FinalTitle, sess.ReportState.FinalAuthor, sess.ReportState)
	sess.ReportState.RUnlock()

	// Block hashes take their own read lock, so they are computed outside the
	// critical section above.
	hashes := sess.ReportState.BlockContentHashes()
	coordinator.Request(version, reason, html, hashes)
}
