package buildinfo

import "testing"

func TestCurrentUsesSourceAgentVersionByDefault(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() {
		Version, Commit, BuildTime = oldVersion, oldCommit, oldBuildTime
	})
	Version, Commit, BuildTime = "", "", ""

	got := Current(2)
	if got.Version != AgentVersion {
		t.Fatalf("version = %q, want source agent version %q", got.Version, AgentVersion)
	}
}

func TestCurrentAllowsExplicitVersionOverride(t *testing.T) {
	oldVersion, oldCommit, oldBuildTime := Version, Commit, BuildTime
	t.Cleanup(func() {
		Version, Commit, BuildTime = oldVersion, oldCommit, oldBuildTime
	})
	Version, Commit, BuildTime = "custom-build", "", ""

	got := Current(2)
	if got.Version != "custom-build" {
		t.Fatalf("version = %q, want explicit override", got.Version)
	}
}
