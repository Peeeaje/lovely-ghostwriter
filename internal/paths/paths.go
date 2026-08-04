package paths

import (
	"os"
	"path/filepath"
)

const AppName = "lovely-ghostwriter"

type Paths struct {
	Root        string
	Config      string
	State       string
	Log         string
	LaunchAgent string
}

func Default() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	configRoot := os.Getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		configRoot = filepath.Join(home, ".config")
	}
	stateRoot := os.Getenv("XDG_STATE_HOME")
	if stateRoot == "" {
		stateRoot = filepath.Join(home, ".local", "state")
	}
	configDir := filepath.Join(configRoot, AppName)
	stateDir := filepath.Join(stateRoot, AppName)
	return Paths{
		Root:        stateDir,
		Config:      filepath.Join(configDir, "config.toml"),
		State:       filepath.Join(stateDir, "state.db"),
		Log:         filepath.Join(stateDir, "lovely-ghostwriter.log"),
		LaunchAgent: filepath.Join(home, "Library", "LaunchAgents", "io.github.peeeaje.lovely-ghostwriter.plist"),
	}, nil
}
