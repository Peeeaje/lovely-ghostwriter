package lock

import (
	"path/filepath"
	"testing"
)

func TestAcquireRejectsSecondConsumer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "consumer.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := Acquire(path); err == nil {
		t.Fatal("second Acquire() succeeded")
	}
}
