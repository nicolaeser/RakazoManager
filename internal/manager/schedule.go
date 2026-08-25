package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/fsutil"
)

const (
	scheduledBackupLabel  = "scheduled"
	scheduleSchemaVersion = 1
	metadataPrefix        = "# RAKAZO-MANAGER BACKUP METADATA "
	maxScheduleKeep       = 1000
	maxScheduleMetadata   = 16 << 10
	maxScheduleLogSize    = 5 << 20
)

type BackupSchedule struct {
	Cron         string `json:"cron"`
	Keep         int    `json:"keep"`
	Executable   string `json:"executable"`
	InstanceRoot string `json:"instance_root"`
	Path         string `json:"path"`
}

type ScheduledBackupResult struct {
	Archive string   `json:"archive"`
	Pruned  []string `json:"pruned,omitempty"`
}

type scheduleMetadata struct {
	BackupSchedule
	SchemaVersion        int  `json:"schema_version"`
	RemoveLeadingNewline bool `json:"remove_leading_newline,omitempty"`
}

type crontabScheduleStore struct {
	runner       command.Runner
	instanceName string
	root         string
	statePath    string
	logPath      string
	removeFile   func(string) error
}

func (manager *Manager) SetBackupSchedule(ctx context.Context, schedule BackupSchedule) (result BackupSchedule, operationErr error) {
	store, err := manager.newCrontabScheduleStore()
	if err != nil {
		return BackupSchedule{}, err
	}
	schedule.InstanceRoot = manager.Paths.Root
	schedule.Path = os.Getenv("PATH")
	schedule, err = normalizeBackupSchedule(schedule, true)
	if err != nil {
		return BackupSchedule{}, err
	}
	if err := manager.Docker.CheckCLI(ctx); err != nil {
		return BackupSchedule{}, fmt.Errorf("validate Docker for scheduled backups: %w", err)
	}
	lock, err := manager.operationLock("schedule-set")
	if err != nil {
		return BackupSchedule{}, err
	}
	defer releaseLock(lock, &operationErr)
	store, err = manager.newCrontabScheduleStore()
	if err != nil {
		return BackupSchedule{}, err
	}
	if err := store.Set(ctx, schedule); err != nil {
		return BackupSchedule{}, err
	}
	_ = manager.StateStore.Log("schedule-set", fmt.Sprintf("cron=%q keep=%d result=success", schedule.Cron, schedule.Keep))
	return schedule, nil
}

func (manager *Manager) ShowBackupSchedule(ctx context.Context) (BackupSchedule, bool, error) {
	store, err := manager.newCrontabScheduleStore()
	if err != nil {
		return BackupSchedule{}, false, err
	}
	return store.Show(ctx)
}

func (manager *Manager) RemoveBackupSchedule(ctx context.Context) (removed bool, operationErr error) {
	store, err := manager.newCrontabScheduleStore()
	if err != nil {
		return false, err
	}
	lock, err := manager.operationLock("schedule-remove")
	if err != nil {
		return false, err
	}
	defer releaseLock(lock, &operationErr)
	store, err = manager.newCrontabScheduleStore()
	if err != nil {
		return false, err
	}
	removed, err = store.Remove(ctx)
	if err != nil {
		return false, err
	}
	if removed {
		_ = manager.StateStore.Log("schedule-remove", "result=success")
	}
	return removed, nil
}

func (manager *Manager) RunScheduledBackup(ctx context.Context, keep int) (result ScheduledBackupResult, operationErr error) {
	if err := validateScheduleKeep(keep); err != nil {
		return ScheduledBackupResult{}, err
	}
	lock, err := manager.operationLock("schedule-run")
	if err != nil {
		return ScheduledBackupResult{}, err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.rotateScheduleLog(); err != nil {
		return ScheduledBackupResult{}, err
	}
	if err := manager.ensureRunning(ctx); err != nil {
		return ScheduledBackupResult{}, err
	}

	archive, err := manager.backupUnlocked(ctx, scheduledBackupLabel)
	result.Archive = archive
	if err != nil {
		return result, err
	}
	pruned, err := manager.pruneBackupsUnlocked(keep, "schedule-prune", scheduledBackupName)
	result.Pruned = pruned
	if err != nil {
		return result, fmt.Errorf("scheduled backup succeeded but retention failed: %w", err)
	}
	_ = manager.StateStore.Log("schedule-run", fmt.Sprintf("file=%s keep=%d pruned=%d result=success", filepath.Base(archive), keep, len(pruned)))
	return result, nil
}

func (manager *Manager) newCrontabScheduleStore() (crontabScheduleStore, error) {
	if err := manager.RequireInstalled(); err != nil {
		return crontabScheduleStore{}, err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return crontabScheduleStore{}, err
	}
	if manager.Docker.Runner == nil {
		return crontabScheduleStore{}, fmt.Errorf("command runner is not configured")
	}
	return crontabScheduleStore{
		runner:       manager.Docker.Runner,
		instanceName: cfg.Name,
		root:         manager.Paths.Root,
		statePath:    manager.Paths.Schedule,
		logPath:      manager.Paths.ScheduleLog,
		removeFile:   os.Remove,
	}, nil
}

func (store crontabScheduleStore) Set(ctx context.Context, schedule BackupSchedule) error {
	previousMetadata, stateFound, err := store.readState()
	if err != nil {
		return err
	}
	existing, err := store.read(ctx)
	if err != nil {
		return err
	}
	_, blockMetadata, blockFound, err := store.findOwnedBlock(existing)
	if err != nil {
		return err
	}
	if stateFound && (!blockFound || previousMetadata != blockMetadata) {
		return fmt.Errorf("backup schedule state does not match the owned crontab block; inspect both before replacing it")
	}
	base, _, err := store.removeOwnedBlock(existing)
	if err != nil {
		return err
	}
	metadata := scheduleMetadata{BackupSchedule: schedule, SchemaVersion: scheduleSchemaVersion}
	if base != "" && !strings.HasSuffix(base, "\n") {
		metadata.RemoveLeadingNewline = true
		base += "\n"
	}
	block, err := store.renderBlock(metadata)
	if err != nil {
		return err
	}
	if err := store.writeState(metadata); err != nil {
		return err
	}
	if err := store.writeIfUnchanged(ctx, existing, base+block); err != nil {
		if rollbackErr := store.restoreState(previousMetadata, stateFound); rollbackErr != nil {
			return fmt.Errorf("%w; restoring previous backup schedule state also failed: %v", err, rollbackErr)
		}
		return err
	}
	return nil
}

func (store crontabScheduleStore) Show(ctx context.Context) (BackupSchedule, bool, error) {
	stateMetadata, stateFound, err := store.readState()
	if err != nil || !stateFound {
		return BackupSchedule{}, stateFound, err
	}
	existing, err := store.read(ctx)
	if err != nil {
		return BackupSchedule{}, false, err
	}
	_, metadata, found, err := store.findOwnedBlock(existing)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("backup schedule state exists but the owned crontab block is missing")
		}
		return BackupSchedule{}, false, err
	}
	if metadata != stateMetadata {
		return BackupSchedule{}, false, fmt.Errorf("backup schedule state does not match the owned crontab block")
	}
	return metadata.BackupSchedule, true, nil
}

func (store crontabScheduleStore) Remove(ctx context.Context) (bool, error) {
	stateMetadata, stateFound, err := store.readState()
	if err != nil || !stateFound {
		return stateFound, err
	}
	existing, err := store.read(ctx)
	if err != nil {
		return false, err
	}
	updated, found, err := store.removeOwnedBlock(existing)
	if err != nil || !found {
		if err == nil {
			err = fmt.Errorf("backup schedule state exists but the owned crontab block is missing")
		}
		return found, err
	}
	_, blockMetadata, _, err := store.findOwnedBlock(existing)
	if err != nil {
		return false, err
	}
	if blockMetadata != stateMetadata {
		return false, fmt.Errorf("backup schedule state does not match the owned crontab block")
	}
	if err := store.writeIfUnchanged(ctx, existing, updated); err != nil {
		return false, err
	}
	if err := store.removeState(); err != nil {
		if rollbackErr := store.writeIfUnchanged(ctx, updated, existing); rollbackErr != nil {
			return false, fmt.Errorf("remove backup schedule state: %w; restoring the owned crontab block also failed: %v", err, rollbackErr)
		}
		return false, fmt.Errorf("remove backup schedule state: %w; the owned crontab block was restored", err)
	}
	return true, nil
}

func (store crontabScheduleStore) readState() (scheduleMetadata, bool, error) {
	pathInfo, err := os.Lstat(store.statePath)
	if os.IsNotExist(err) {
		return scheduleMetadata{}, false, nil
	}
	if err != nil {
		return scheduleMetadata{}, false, fmt.Errorf("inspect backup schedule state: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Size() > maxScheduleMetadata {
		return scheduleMetadata{}, false, fmt.Errorf("backup schedule state must be a bounded real regular file")
	}
	file, err := os.Open(store.statePath)
	if err != nil {
		return scheduleMetadata{}, false, fmt.Errorf("open backup schedule state: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return scheduleMetadata{}, false, fmt.Errorf("inspect backup schedule state: %w", err)
	}
	if !os.SameFile(pathInfo, info) || !info.Mode().IsRegular() || info.Size() > maxScheduleMetadata {
		return scheduleMetadata{}, false, fmt.Errorf("backup schedule state changed while it was being opened")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxScheduleMetadata+1))
	if err != nil {
		return scheduleMetadata{}, false, fmt.Errorf("read backup schedule state: %w", err)
	}
	if len(content) > maxScheduleMetadata {
		return scheduleMetadata{}, false, fmt.Errorf("backup schedule state exceeds the supported size")
	}
	metadata, err := decodeScheduleMetadata(content)
	if err != nil {
		return scheduleMetadata{}, false, fmt.Errorf("parse backup schedule state: %w", err)
	}
	if metadata.InstanceRoot != store.root {
		return scheduleMetadata{}, false, fmt.Errorf("backup schedule state targets %s instead of %s", metadata.InstanceRoot, store.root)
	}
	return metadata, true, nil
}

func (store crontabScheduleStore) writeState(metadata scheduleMetadata) error {
	content, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode backup schedule state: %w", err)
	}
	content = append(content, '\n')
	if len(content) > maxScheduleMetadata {
		return fmt.Errorf("backup schedule state exceeds the supported size")
	}
	if err := fsutil.AtomicWriteFile(store.statePath, content, 0o600); err != nil {
		return fmt.Errorf("write backup schedule state: %w", err)
	}
	return nil
}

func (store crontabScheduleStore) restoreState(metadata scheduleMetadata, found bool) error {
	if found {
		return store.writeState(metadata)
	}
	return store.removeState()
}

func (store crontabScheduleStore) removeState() error {
	remove := store.removeFile
	if remove == nil {
		remove = os.Remove
	}
	if err := remove(store.statePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (store crontabScheduleStore) read(ctx context.Context) (string, error) {
	result, err := store.runner.Run(ctx, command.Request{
		Name:    "crontab",
		Args:    []string{"-l"},
		Capture: true,
	})
	if err == nil {
		return result.Stdout, nil
	}
	detail := strings.ToLower(result.Stderr + " " + err.Error())
	if strings.Contains(detail, "no crontab for") {
		return "", nil
	}
	return "", fmt.Errorf("read current crontab: %w", err)
}

func (store crontabScheduleStore) write(ctx context.Context, content string) error {
	_, err := store.runner.Run(ctx, command.Request{
		Name:    "crontab",
		Args:    []string{"-"},
		Stdin:   strings.NewReader(content),
		Capture: true,
	})
	if err != nil {
		return fmt.Errorf("install updated crontab: %w", err)
	}
	return nil
}

func (store crontabScheduleStore) writeIfUnchanged(ctx context.Context, expected, content string) error {
	current, err := store.read(ctx)
	if err != nil {
		return err
	}
	if current != expected {
		return fmt.Errorf("current crontab changed while the backup schedule was being updated; retry without overwriting the concurrent change")
	}
	return store.write(ctx, content)
}

type ownedBlock struct {
	start int
	end   int
}

func (store crontabScheduleStore) findOwnedBlock(content string) (ownedBlock, scheduleMetadata, bool, error) {
	begin, end := store.markers()
	type line struct {
		start int
		end   int
		text  string
	}
	var lines []line
	for start := 0; start < len(content); {
		relativeEnd := strings.IndexByte(content[start:], '\n')
		lineEnd := len(content)
		next := len(content)
		if relativeEnd >= 0 {
			lineEnd = start + relativeEnd
			next = lineEnd + 1
		}
		lines = append(lines, line{start: start, end: next, text: content[start:lineEnd]})
		start = next
	}

	beginIndex, endIndex := -1, -1
	for index, current := range lines {
		switch current.text {
		case begin:
			if beginIndex >= 0 {
				return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("multiple owned backup schedule blocks found")
			}
			beginIndex = index
		case end:
			if endIndex >= 0 {
				return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("multiple owned backup schedule end markers found")
			}
			endIndex = index
		}
	}
	if beginIndex < 0 && endIndex < 0 {
		return ownedBlock{}, scheduleMetadata{}, false, nil
	}
	if beginIndex < 0 || endIndex < 0 || endIndex <= beginIndex {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("owned backup schedule block has incomplete or misordered markers")
	}
	if endIndex-beginIndex != 3 {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("owned backup schedule block has unexpected content")
	}

	metadataLine := lines[beginIndex+1].text
	if !strings.HasPrefix(metadataLine, metadataPrefix) {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("owned backup schedule metadata is missing")
	}
	encoded := strings.TrimPrefix(metadataLine, metadataPrefix)
	if len(encoded) == 0 || len(encoded) > maxScheduleMetadata {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("owned backup schedule metadata is empty or too large")
	}
	metadataJSON, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("decode owned backup schedule metadata: %w", err)
	}
	metadata, err := decodeScheduleMetadata(metadataJSON)
	if err != nil {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("parse owned backup schedule metadata: %w", err)
	}
	if metadata.InstanceRoot != store.root {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("owned backup schedule targets %s instead of %s", metadata.InstanceRoot, store.root)
	}
	if lines[beginIndex+2].text != store.renderCommand(metadata.BackupSchedule) {
		return ownedBlock{}, scheduleMetadata{}, false, fmt.Errorf("owned backup schedule command was modified")
	}
	return ownedBlock{start: lines[beginIndex].start, end: lines[endIndex].end}, metadata, true, nil
}

func decodeScheduleMetadata(content []byte) (scheduleMetadata, error) {
	var metadata scheduleMetadata
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return scheduleMetadata{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return scheduleMetadata{}, fmt.Errorf("trailing content")
	}
	if metadata.SchemaVersion != scheduleSchemaVersion {
		return scheduleMetadata{}, fmt.Errorf("unsupported backup schedule schema %d", metadata.SchemaVersion)
	}
	var err error
	metadata.BackupSchedule, err = normalizeBackupSchedule(metadata.BackupSchedule, false)
	if err != nil {
		return scheduleMetadata{}, fmt.Errorf("validate backup schedule: %w", err)
	}
	return metadata, nil
}

func (store crontabScheduleStore) removeOwnedBlock(content string) (string, bool, error) {
	block, metadata, found, err := store.findOwnedBlock(content)
	if err != nil || !found {
		return content, found, err
	}
	start := block.start
	if metadata.RemoveLeadingNewline {
		if start == 0 || content[start-1] != '\n' {
			return content, false, fmt.Errorf("owned backup schedule separator was modified")
		}
		start--
	}
	return content[:start] + content[block.end:], true, nil
}

func (store crontabScheduleStore) renderBlock(metadata scheduleMetadata) (string, error) {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("encode backup schedule metadata: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(metadataJSON)
	if len(encoded) > maxScheduleMetadata {
		return "", fmt.Errorf("backup schedule metadata exceeds the supported size")
	}
	begin, end := store.markers()
	return strings.Join([]string{
		begin,
		metadataPrefix + encoded,
		store.renderCommand(metadata.BackupSchedule),
		end,
		"",
	}, "\n"), nil
}

func (store crontabScheduleStore) renderCommand(schedule BackupSchedule) string {
	commandLine := strings.Join([]string{
		shellQuote(schedule.Executable),
		"--yes",
		"--no-color",
		"schedule",
		"run",
		"--keep",
		strconv.Itoa(schedule.Keep),
		shellQuote(store.root),
	}, " ")
	return schedule.Cron + " PATH=" + shellQuote(schedule.Path) + "; export PATH; umask 077; " + commandLine + " >> " + shellQuote(store.logPath) + " 2>&1"
}

func (store crontabScheduleStore) markers() (string, string) {
	return "# BEGIN RAKAZO-MANAGER BACKUP " + store.instanceName,
		"# END RAKAZO-MANAGER BACKUP " + store.instanceName
}

func normalizeBackupSchedule(schedule BackupSchedule, inspectExecutable bool) (BackupSchedule, error) {
	if err := validateCron(schedule.Cron); err != nil {
		return BackupSchedule{}, err
	}
	if err := validateScheduleKeep(schedule.Keep); err != nil {
		return BackupSchedule{}, err
	}
	for label, value := range map[string]string{
		"executable":    schedule.Executable,
		"instance root": schedule.InstanceRoot,
	} {
		if value == "" || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return BackupSchedule{}, fmt.Errorf("%s must be a non-empty value without surrounding whitespace or control characters", label)
		}
		if strings.Contains(value, "%") {
			return BackupSchedule{}, fmt.Errorf("%s must not contain %% because crontab treats it as input data", label)
		}
		if !filepath.IsAbs(value) {
			return BackupSchedule{}, fmt.Errorf("%s must be an absolute path", label)
		}
	}
	schedule.Executable = filepath.Clean(schedule.Executable)
	schedule.InstanceRoot = filepath.Clean(schedule.InstanceRoot)
	if schedule.Path == "" || len(schedule.Path) > 8192 || strings.TrimSpace(schedule.Path) != schedule.Path || strings.IndexFunc(schedule.Path, unicode.IsControl) >= 0 || strings.Contains(schedule.Path, "%") {
		return BackupSchedule{}, fmt.Errorf("PATH must be a non-empty bounded value without surrounding whitespace, control characters, or %%")
	}
	for _, directory := range filepath.SplitList(schedule.Path) {
		if directory == "" || !filepath.IsAbs(directory) {
			return BackupSchedule{}, fmt.Errorf("every scheduled PATH entry must be an absolute directory")
		}
	}
	if inspectExecutable {
		info, err := os.Stat(schedule.Executable)
		if err != nil {
			return BackupSchedule{}, fmt.Errorf("inspect scheduled executable: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return BackupSchedule{}, fmt.Errorf("scheduled executable is not an executable regular file")
		}
	}
	return schedule, nil
}

func (manager *Manager) rotateScheduleLog() error {
	info, err := os.Lstat(manager.Paths.ScheduleLog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect scheduled backup log: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("scheduled backup log must be a regular file: %s", manager.Paths.ScheduleLog)
	}
	if info.Size() <= maxScheduleLogSize {
		return nil
	}
	if err := os.Truncate(manager.Paths.ScheduleLog, 0); err != nil {
		return fmt.Errorf("truncate oversized scheduled backup log: %w", err)
	}
	return nil
}

func validateCron(value string) error {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.Join(strings.Fields(value), " ") != value {
		return fmt.Errorf("cron expression must contain exactly five fields separated by single spaces")
	}
	fields := strings.Split(value, " ")
	if len(fields) != 5 {
		return fmt.Errorf("cron expression must contain exactly five fields")
	}
	for _, field := range fields {
		if field == "" || len(field) > 64 {
			return fmt.Errorf("cron field is empty or too long")
		}
		for _, character := range field {
			allowed := character >= '0' && character <= '9' ||
				character >= 'A' && character <= 'Z' ||
				character >= 'a' && character <= 'z' ||
				strings.ContainsRune("*/,-", character)
			if !allowed {
				return fmt.Errorf("cron expression contains unsupported character %q", character)
			}
		}
	}
	return nil
}

func validateScheduleKeep(keep int) error {
	if keep < 1 || keep > maxScheduleKeep {
		return fmt.Errorf("scheduled backup retention must be between 1 and %d", maxScheduleKeep)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
