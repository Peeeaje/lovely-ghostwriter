package reviewer

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestConfigureProcessGroupStopsChildProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "trap 'exit 0' TERM; /bin/sh -c \"trap '' TERM; /bin/sleep 30\" & wait")
	configureProcessGroup(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	cancel()
	started := time.Now()
	err := command.Wait()
	if err == nil {
		t.Fatal("canceled command succeeded")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("process group did not stop promptly")
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still exists: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
