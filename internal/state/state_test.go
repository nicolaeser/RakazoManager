package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/stack"
)

func TestClearStaleLockRemovesDeadLockAndNoopsWhenAbsent(t *testing.T) {
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Paths: paths}
	if err := os.MkdirAll(paths.Manager, 0o700); err != nil {
		t.Fatal(err)
	}

	cleared, detail, err := store.ClearStaleLock()
	if err != nil || cleared || detail == "" {
		t.Fatalf("absent lock: cleared=%v detail=%q err=%v", cleared, detail, err)
	}

	if err := os.WriteFile(paths.Lock, []byte("pid=2147483646 operation=test started=2020-01-01T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleared, detail, err = store.ClearStaleLock()
	if err != nil || !cleared {
		t.Fatalf("stale lock: cleared=%v detail=%q err=%v", cleared, detail, err)
	}
	if _, err := os.Lstat(paths.Lock); !os.IsNotExist(err) {
		t.Fatalf("lock file still present: %v", err)
	}
	if detail == "" {
		t.Fatal("expected previous lock detail")
	}
}

func TestClearStaleLockRefusesLiveProcess(t *testing.T) {
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Paths: paths}
	if err := os.MkdirAll(paths.Manager, 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("pid=%d operation=test started=2020-01-01T00:00:00Z\n", os.Getpid())
	if err := os.WriteFile(paths.Lock, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cleared, _, err := store.ClearStaleLock()
	if err == nil || cleared {
		t.Fatalf("expected refusal for live pid, cleared=%v err=%v", cleared, err)
	}
	if _, err := os.Lstat(paths.Lock); err != nil {
		t.Fatalf("live lock should remain: %v", err)
	}

	if filepath.Dir(paths.Lock) != paths.Manager {
		t.Fatalf("unexpected lock path %s", paths.Lock)
	}
}
