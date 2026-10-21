package session

import "testing"

// Background jobs are wired per session: the job tools must be registered and
// the manager must be reachable from the session that owns the tool registry.
func TestSessionWiresBackgroundJobs(t *testing.T) {
	sess, err := New("s_jobs", "ws_1", "u_1", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if sess.Jobs == nil {
		t.Fatalf("expected a job manager on the session")
	}
	if sess.Registry == nil {
		t.Fatalf("expected a tool registry on the session")
	}
	for _, name := range []string{"job_submit", "state_job_inspect"} {
		if _, err := sess.Registry.Get(name); err != nil {
			t.Fatalf("expected %s to be registered for the session: %v", name, err)
		}
	}
}
