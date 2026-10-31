package buildinfo

import (
	"runtime"
	"testing"
)

func TestDevelopmentIdentity(t *testing.T) {
	got := Current()
	if got.Version != "dev" || got.Commit != "unknown" || got.Architecture != runtime.GOARCH {
		t.Fatalf("unexpected default: %+v", got)
	}
}
func TestReleaseIdentity(t *testing.T) {
	oldVersion, oldCommit := version, commit
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	version, commit = "1.2.3-rc.1", "0123456789012345678901234567890123456789"
	got := Current()
	if got.Version != version || got.Commit != commit {
		t.Fatalf("missing release identity: %+v", got)
	}
}
