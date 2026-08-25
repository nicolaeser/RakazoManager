package manager

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneAutomaticBackupsExpectedRefusesChangedPlan(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))

	oldest := filepath.Join(manager.Paths.Backups, "rakazo-pre-update-20200101T000000.000Z.zip")
	newest := filepath.Join(manager.Paths.Backups, "rakazo-pre-update-20200102T000000.000Z.zip")
	writeTestZip(t, oldest)
	writeTestZip(t, newest)
	setBackupTime(t, oldest, time.Unix(1, 0))
	setBackupTime(t, newest, time.Unix(2, 0))

	plan, err := manager.BackupsToPrune(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0] != filepath.Base(oldest) {
		t.Fatalf("unexpected initial plan: %v", plan)
	}

	concurrent := filepath.Join(manager.Paths.Backups, "rakazo-pre-restore-20200103T000000.000Z.zip")
	writeTestZip(t, concurrent)
	setBackupTime(t, concurrent, time.Unix(3, 0))
	if _, err := manager.PruneAutomaticBackupsExpected(1, plan); err == nil {
		t.Fatal("expected changed-plan refusal")
	}
	for _, candidate := range []string{oldest, newest, concurrent} {
		if _, err := os.Stat(candidate); err != nil {
			t.Fatalf("changed plan deleted %s: %v", candidate, err)
		}
	}

	current, err := manager.BackupsToPrune(1)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := manager.PruneAutomaticBackupsExpected(1, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted %v, want two reviewed candidates", deleted)
	}
	if _, err := os.Stat(concurrent); err != nil {
		t.Fatalf("newest backup was not retained: %v", err)
	}
	for _, candidate := range []string{oldest, newest} {
		if _, err := os.Stat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reviewed candidate still exists: %s: %v", candidate, err)
		}
	}
}

func setBackupTime(t *testing.T, path string, value time.Time) {
	t.Helper()
	if err := os.Chtimes(path, value, value); err != nil {
		t.Fatal(err)
	}
}
