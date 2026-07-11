package supervisor

import "testing"

// TestManagedAgent_LaunchPIDSurvivesExit pins the invariant the spawn-response
// path relies on: ClearPID (called when a process exits) zeroes the live PID,
// but the launch PID remains readable so a short-lived agent's spawn response
// can still report the PID it launched instead of 0.
func TestManagedAgent_LaunchPIDSurvivesExit(t *testing.T) {
	m := NewManagedAgent("guild-1", "agent-1")

	m.SetPID(4242)
	if got := m.GetPID(); got != 4242 {
		t.Fatalf("GetPID after launch = %d, want 4242", got)
	}
	if got := m.GetLaunchPID(); got != 4242 {
		t.Fatalf("GetLaunchPID after launch = %d, want 4242", got)
	}

	// Process exits: the monitor clears the live PID.
	m.ClearPID()
	if got := m.GetPID(); got != 0 {
		t.Errorf("GetPID after exit = %d, want 0", got)
	}
	if got := m.GetLaunchPID(); got != 4242 {
		t.Errorf("GetLaunchPID after exit = %d, want 4242 (must survive exit)", got)
	}

	// A restart assigns a new PID; the launch PID tracks the latest launch.
	m.SetPID(5353)
	if got := m.GetLaunchPID(); got != 5353 {
		t.Errorf("GetLaunchPID after restart = %d, want 5353", got)
	}
}
