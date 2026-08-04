package paths

import (
	"fmt"
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
	configDir, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	root := filepath.Join(configDir, AppName)
	return Paths{
		Root:        root,
		Config:      filepath.Join(root, "config.toml"),
		State:       filepath.Join(root, "state.db"),
		Log:         filepath.Join(root, "lovely-ghostwriter.log"),
		LaunchAgent: filepath.Join(home, "Library", "LaunchAgents", "io.github.peeeaje.lovely-ghostwriter.plist"),
	}, nil
}
