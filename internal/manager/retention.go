package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type backupCandidate struct {
	Name    string
	ModTime int64
}

func (manager *Manager) BackupsToPrune(keep int) ([]string, error) {
	return manager.backupsToPrune(keep, automaticBackupName)
}

func (manager *Manager) backupsToPrune(keep int, eligible func(string) bool) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("keep must be at least 1")
	}
	if err := manager.RequireInstalled(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(manager.Paths.Backups)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var candidates []backupCandidate
	for _, entry := range entries {
		if entry.IsDir() || !eligible(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect backup %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		candidates = append(candidates, backupCandidate{
			Name:    entry.Name(),
			ModTime: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].ModTime == candidates[right].ModTime {
			return candidates[left].Name > candidates[right].Name
		}
		return candidates[left].ModTime > candidates[right].ModTime
	})
	if len(candidates) <= keep {
		return nil, nil
	}
	files := make([]string, 0, len(candidates)-keep)
	for _, candidate := range candidates[keep:] {
		files = append(files, candidate.Name)
	}
	return files, nil
}

func (manager *Manager) PruneAutomaticBackups(keep int) (deleted []string, operationErr error) {
	return manager.pruneBackups(keep, "backup-prune", automaticBackupName)
}

func (manager *Manager) PruneAutomaticBackupsExpected(keep int, expected []string) (deleted []string, operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return nil, err
	}
	lock, err := manager.operationLock("backup-prune")
	if err != nil {
		return nil, err
	}
	defer releaseLock(lock, &operationErr)

	actual, err := manager.backupsToPrune(keep, automaticBackupName)
	if err != nil {
		return nil, err
	}
	if !sameBackupNames(actual, expected) {
		return nil, fmt.Errorf("automatic backup set changed after review; inspect the prune plan again")
	}
	return manager.deleteBackupCandidates(actual, keep, "backup-prune", automaticBackupName)
}

func (manager *Manager) pruneBackups(keep int, operation string, eligible func(string) bool) (deleted []string, operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return nil, err
	}
	lock, err := manager.operationLock(operation)
	if err != nil {
		return nil, err
	}
	defer releaseLock(lock, &operationErr)

	return manager.pruneBackupsUnlocked(keep, operation, eligible)
}

func (manager *Manager) pruneBackupsUnlocked(keep int, operation string, eligible func(string) bool) (deleted []string, resultErr error) {
	files, err := manager.backupsToPrune(keep, eligible)
	if err != nil {
		return nil, err
	}
	return manager.deleteBackupCandidates(files, keep, operation, eligible)
}

func (manager *Manager) deleteBackupCandidates(files []string, keep int, operation string, eligible func(string) bool) (deleted []string, resultErr error) {
	for _, name := range files {
		if !eligible(name) || filepath.Base(name) != name {
			return deleted, fmt.Errorf("refuse unsafe backup name %q", name)
		}
		if err := os.Remove(filepath.Join(manager.Paths.Backups, name)); err != nil {
			return deleted, fmt.Errorf("delete backup %s: %w", name, err)
		}
		deleted = append(deleted, name)
	}
	_ = manager.StateStore.Log(operation, fmt.Sprintf("keep=%d deleted=%d result=success", keep, len(deleted)))
	return deleted, nil
}

func sameBackupNames(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func scheduledBackupName(name string) bool {
	return strings.HasPrefix(name, "rakazo-scheduled-") && strings.HasSuffix(strings.ToLower(name), ".zip")
}

func automaticBackupName(name string) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return false
	}
	for _, prefix := range []string{
		"rakazo-pre-update-",
		"rakazo-pre-restore-",
		"rakazo-pre-image-rollback-",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return scheduledBackupName(name)
}
