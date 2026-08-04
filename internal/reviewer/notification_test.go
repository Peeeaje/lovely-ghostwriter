package reviewer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Peeeaje/lovely-ghostwriter/internal/config"
	"github.com/Peeeaje/lovely-ghostwriter/internal/state"
)

func TestNotifyStartedIncludesPullRequestURL(t *testing.T) {
	output := filepath.Join(t.TempDir(), "args")
	command := filepath.Join(t.TempDir(), "notifier")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + output + "\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Config: config.Config{Notification: config.NotificationConfig{
		Enabled: true, Command: command, Timeout: "1s", Started: true,
	}}}
	pr := state.PullRequest{Repository: "owner/repository", Number: 42, Title: "Change", URL: "https://example.test/pull/42"}
	if err := runner.NotifyStarted(context.Background(), pr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), pr.URL) || !strings.Contains(string(data), "owner/repository#42") {
		t.Fatalf("notification args = %s", data)
	}
}

func TestNotifyStartedTimesOut(t *testing.T) {
	command := filepath.Join(t.TempDir(), "notifier")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexec /bin/sleep 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Config: config.Config{Notification: config.NotificationConfig{
		Enabled: true, Command: command, Timeout: "20ms", Started: true,
	}}}
	started := time.Now()
	if err := runner.NotifyStarted(context.Background(), state.PullRequest{}); err == nil {
		t.Fatal("NotifyStarted() succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("NotifyStarted() did not honor its timeout")
	}
}
