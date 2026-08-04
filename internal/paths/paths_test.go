package paths

import (
	"path/filepath"
	"testing"
)

func TestDefaultUsesXDGDirectories(t *testing.T) {
	configHome := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)

	paths, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	if paths.Config != filepath.Join(configHome, AppName, "config.toml") {
		t.Fatalf("config path = %s", paths.Config)
	}
	if paths.State != filepath.Join(stateHome, AppName, "state.db") {
		t.Fatalf("state path = %s", paths.State)
	}
	if paths.Log != filepath.Join(stateHome, AppName, "lovely-ghostwriter.log") {
		t.Fatalf("log path = %s", paths.Log)
	}
}
