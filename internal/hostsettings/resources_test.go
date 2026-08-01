package hostsettings

import "testing"

// The mode has to change what a project actually costs. A setting that resolves
// to the same numbers in every mode is a setting that does nothing.
func TestEachModeCostsSomethingDifferent(t *testing.T) {
	quiet := Resources{Mode: ModeQuiet}.Budget()
	normal := Resources{Mode: ModeNormal}.Budget()
	fast := Resources{Mode: ModeFast}.Budget()

	if !(quiet.CPUs > 0 && normal.CPUs > quiet.CPUs) {
		t.Fatalf("quiet %v and normal %v do not differ in CPU", quiet.CPUs, normal.CPUs)
	}
	if fast.CPUs != 0 {
		t.Fatalf("fast CPUs = %v, want no limit", fast.CPUs)
	}
	if !(quiet.IndexParallelism > 0 && normal.IndexParallelism > quiet.IndexParallelism) {
		t.Fatalf("quiet %d and normal %d do not differ in index parallelism",
			quiet.IndexParallelism, normal.IndexParallelism)
	}
	if fast.IndexParallelism != 0 {
		t.Fatalf("fast parallelism = %d, want the engine's own sizing", fast.IndexParallelism)
	}
}

// An unset mode resolves to the shipped balance rather than to no limits, so a
// document that forgets the field does not quietly hand over the machine.
func TestAnUnsetModeIsTheBalancedOne(t *testing.T) {
	unset := Resources{}.Budget()
	normal := Resources{Mode: ModeNormal}.Budget()
	if unset != normal {
		t.Fatalf("an unset mode resolves to %+v, want the balanced %+v", unset, normal)
	}
}

// Memory is opt-in for a reason worth pinning down: a CPU limit slows an indexer,
// a memory limit kills it.
func TestMemoryIsUnlimitedUnlessAskedFor(t *testing.T) {
	for _, mode := range []Mode{ModeQuiet, ModeNormal, ModeFast} {
		if got := (Resources{Mode: mode}).Budget().MemoryLimitMB; got != 0 {
			t.Errorf("mode %q caps memory at %d MB without being asked", mode, got)
		}
	}
	if got := (Resources{Mode: ModeQuiet, MemoryLimitMB: 512}).Budget().MemoryLimitMB; got != 512 {
		t.Errorf("an explicit memory limit was not applied: %d", got)
	}
}

func TestAProjectModeOverridesTheMachinePolicy(t *testing.T) {
	machine := Resources{Mode: ModeFast, MemoryLimitMB: 2048}

	quiet := machine.WithProjectMode(ModeQuiet)
	if quiet.Mode != ModeQuiet {
		t.Fatalf("mode = %q, want the project's own", quiet.Mode)
	}
	if quiet.MemoryLimitMB != 2048 {
		t.Fatalf("memory limit = %d, want the machine's to survive", quiet.MemoryLimitMB)
	}
	if got := machine.WithProjectMode("").Mode; got != ModeFast {
		t.Fatalf("an absent override changed the mode to %q", got)
	}
	if got := machine.WithProjectMode("greedy").Mode; got != ModeFast {
		t.Fatalf("an unknown mode was accepted: %q", got)
	}
}

func TestAnUnknownModeInTheSettingsFileIsRefused(t *testing.T) {
	if _, err := LoadFrom(writeSettings(t, `{"resources":{"mode":"turbo"}}`)); err == nil {
		t.Fatal("an unknown resource mode was accepted")
	}
	if _, err := LoadFrom(writeSettings(t, `{"resources":{"memory_limit_mb":-1}}`)); err == nil {
		t.Fatal("a negative memory limit was accepted")
	}
}
