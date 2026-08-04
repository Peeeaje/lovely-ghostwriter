package service

import (
	"strings"
	"testing"
)

func TestLaunchAgentPlistIncludesCommandPath(t *testing.T) {
	plist := launchAgentPlist("/nix/store/app", "/tmp/config.toml", "/tmp/app.log", "/nix/bin:/opt/homebrew/bin")
	if !strings.Contains(plist, "<key>PATH</key>") || !strings.Contains(plist, "/nix/bin:/opt/homebrew/bin") {
		t.Fatalf("plist does not include PATH: %s", plist)
	}
}
