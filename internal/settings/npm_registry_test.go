package settings

import (
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestPreferenceSaveCannotClearANewerNPMRegistry(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	stale := Load()
	const upstream = "https://registry.example/repo"
	if _, err := SetNPMRegistry(upstream); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = SetNPMRegistry("") })
	stale.NoBird = !stale.NoBird
	before, err := SavePrefs(&stale, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := Load(); before != upstream || stale.NPMRegistry != upstream || got.NPMRegistry != upstream || got.NoBird != stale.NoBird {
		t.Fatalf("saving a different preference: before %q, response %q, persisted %q; noBird %v", before, stale.NPMRegistry, got.NPMRegistry, got.NoBird)
	}
	stale.NPMRegistry = ""
	if _, err := SavePrefs(&stale, true); err != nil {
		t.Fatal(err)
	}
	if got := Load().NPMRegistry; got != "" {
		t.Fatalf("explicit reset kept %q", got)
	}
}
