package manager

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/command"
)

type crontabRunner struct {
	content  string
	exists   bool
	listErr  error
	listText string
	writeErr error
	reads    int
	writes   int
	mutateAt int
	mutation string
	onDocker func()
}

func (runner *crontabRunner) Run(_ context.Context, request command.Request) (command.Result, error) {
	if request.Name == "docker" {
		if runner.onDocker != nil {
			callback := runner.onDocker
			runner.onDocker = nil
			callback()
		}
		switch strings.Join(request.Args, " ") {
		case "--version", "compose version":
			return command.Result{Stdout: "test"}, nil
		}
	}
	if request.Name != "crontab" {
		return command.Result{}, errors.New("unexpected executable")
	}
	switch strings.Join(request.Args, " ") {
	case "-l":
		runner.reads++
		if runner.mutateAt == runner.reads {
			runner.content = runner.mutation
			runner.exists = true
		}
		if runner.listErr != nil {
			return command.Result{Stderr: runner.listText}, runner.listErr
		}
		if !runner.exists {
			return command.Result{Stderr: "crontab: no crontab for test"}, errors.New("exit status 1")
		}
		return command.Result{Stdout: runner.content}, nil
	case "-":
		if runner.writeErr != nil {
			return command.Result{Stderr: runner.writeErr.Error()}, runner.writeErr
		}
		content, err := io.ReadAll(request.Stdin)
		if err != nil {
			return command.Result{}, err
		}
		runner.content = string(content)
		runner.exists = true
		runner.writes++
		return command.Result{}, nil
	default:
		return command.Result{}, errors.New("unexpected crontab arguments")
	}
}

func TestBackupScheduleSetShowReplaceRemovePreservesCrontab(t *testing.T) {
	initial := "SHELL=/bin/sh\n0 1 * * * /usr/bin/unrelated --flag\n# BEGIN RAKAZO-MANAGER BACKUP another-instance\n"
	runner := &crontabRunner{content: initial, exists: true}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	executable := executableWithSpecialPath(t)

	set, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "15 4 * * MON",
		Keep:       7,
		Executable: executable,
	})
	if err != nil {
		t.Fatalf("SetBackupSchedule: %v", err)
	}
	if !strings.HasPrefix(runner.content, initial) {
		t.Fatalf("unrelated crontab changed:\n%s", runner.content)
	}
	if strings.Count(runner.content, "# BEGIN RAKAZO-MANAGER BACKUP test-instance") != 1 {
		t.Fatalf("owned marker count is wrong:\n%s", runner.content)
	}
	stateInfo, err := os.Stat(manager.Paths.Schedule)
	if err != nil || stateInfo.Mode().Perm() != 0o600 {
		t.Fatalf("schedule state permissions: info=%v err=%v", stateInfo, err)
	}
	for _, expected := range []string{
		"--yes --no-color schedule run --keep 7",
		shellQuote(executable),
		shellQuote(manager.Paths.Root),
		"PATH=" + shellQuote(set.Path) + "; export PATH",
		shellQuote(manager.Paths.ScheduleLog) + " 2>&1",
	} {
		if !strings.Contains(runner.content, expected) {
			t.Fatalf("generated crontab lacks %q:\n%s", expected, runner.content)
		}
	}

	shown, found, err := manager.ShowBackupSchedule(context.Background())
	if err != nil || !found {
		t.Fatalf("ShowBackupSchedule: found=%t err=%v", found, err)
	}
	if shown != set || shown.InstanceRoot != manager.Paths.Root {
		t.Fatalf("shown schedule = %#v, set = %#v", shown, set)
	}

	_, err = manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 2 * * *",
		Keep:       3,
		Executable: executable,
	})
	if err != nil {
		t.Fatalf("replace schedule: %v", err)
	}
	if strings.Count(runner.content, "# BEGIN RAKAZO-MANAGER BACKUP test-instance") != 1 || !strings.Contains(runner.content, "--keep 3") {
		t.Fatalf("schedule was not replaced exactly:\n%s", runner.content)
	}

	removed, err := manager.RemoveBackupSchedule(context.Background())
	if err != nil || !removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.content != initial {
		t.Fatalf("crontab after removal = %q, want %q", runner.content, initial)
	}
	if _, err := os.Stat(manager.Paths.Schedule); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("schedule state remains after removal: %v", err)
	}
}

func TestBackupScheduleShowAndRemoveSkipCrontabWhenNotConfigured(t *testing.T) {
	runner := &crontabRunner{listErr: errors.New("crontab unavailable"), listText: "permission denied"}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	if schedule, found, err := manager.ShowBackupSchedule(context.Background()); err != nil || found || schedule != (BackupSchedule{}) {
		t.Fatalf("ShowBackupSchedule: schedule=%#v found=%t err=%v", schedule, found, err)
	}
	if removed, err := manager.RemoveBackupSchedule(context.Background()); err != nil || removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.reads != 0 || runner.writes != 0 {
		t.Fatalf("unconfigured schedule invoked crontab: reads=%d writes=%d", runner.reads, runner.writes)
	}
}

func TestBackupScheduleRollsBackStateWhenCrontabInstallFails(t *testing.T) {
	runner := &crontabRunner{writeErr: errors.New("crontab rejected input")}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 3 * * *",
		Keep:       5,
		Executable: testExecutable(t, t.TempDir()),
	})
	if err == nil || !strings.Contains(err.Error(), "install updated crontab") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(manager.Paths.Schedule); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed crontab install left schedule state: %v", statErr)
	}
}

func TestBackupScheduleReplacementRestoresPreviousStateWhenCrontabInstallFails(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	executable := testExecutable(t, t.TempDir())
	want, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{Cron: "0 3 * * *", Keep: 5, Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	previousCrontab := runner.content
	runner.writeErr = errors.New("crontab rejected replacement")
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{Cron: "15 4 * * *", Keep: 2, Executable: executable}); err == nil {
		t.Fatal("expected replacement failure")
	}
	runner.writeErr = nil
	got, found, err := manager.ShowBackupSchedule(context.Background())
	if err != nil || !found || got != want {
		t.Fatalf("previous state was not restored: got=%#v found=%t err=%v", got, found, err)
	}
	if runner.content != previousCrontab {
		t.Fatal("failed replacement changed the crontab")
	}
}

func TestBackupScheduleRefusesConcurrentCrontabChange(t *testing.T) {
	initial := "0 1 * * * /usr/bin/first\n"
	concurrent := initial + "0 2 * * * /usr/bin/concurrent\n"
	runner := &crontabRunner{content: initial, exists: true, mutateAt: 2, mutation: concurrent}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: testExecutable(t, t.TempDir()),
	})
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("error = %v", err)
	}
	if runner.content != concurrent || runner.writes != 0 {
		t.Fatalf("concurrent crontab was overwritten: content=%q writes=%d", runner.content, runner.writes)
	}
	if _, statErr := os.Stat(manager.Paths.Schedule); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("concurrent update left schedule state: %v", statErr)
	}
}

func TestBackupScheduleRefreshesInstanceNameAfterPreflight(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	runner.onDocker = func() {
		cfg, err := manager.ConfigStore.Load()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Name = "renamed-instance"
		if err := manager.ConfigStore.Save(cfg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: testExecutable(t, t.TempDir()),
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.content, "# BEGIN RAKAZO-MANAGER BACKUP renamed-instance\n") || strings.Contains(runner.content, "BACKUP test-instance") {
		t.Fatalf("schedule used stale instance marker:\n%s", runner.content)
	}
}

func TestBackupScheduleRemoveRestoresCrontabWhenStateRemovalFails(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	want, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: testExecutable(t, t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	previousCrontab := runner.content
	store, err := manager.newCrontabScheduleStore()
	if err != nil {
		t.Fatal(err)
	}
	store.removeFile = func(string) error { return errors.New("injected state removal failure") }
	removed, err := store.Remove(context.Background())
	if err == nil || removed || !strings.Contains(err.Error(), "crontab block was restored") {
		t.Fatalf("Remove: removed=%t err=%v", removed, err)
	}
	if runner.content != previousCrontab {
		t.Fatal("state-removal failure did not restore the crontab")
	}
	got, found, showErr := manager.ShowBackupSchedule(context.Background())
	if showErr != nil || !found || got != want {
		t.Fatalf("schedule pair was not restored: got=%#v found=%t err=%v", got, found, showErr)
	}
}

func TestBackupScheduleCorruptStateFailsBeforeCrontabMutation(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: testExecutable(t, t.TempDir()),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.Paths.Schedule, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	readsBefore, writesBefore := runner.reads, runner.writes
	if _, _, err := manager.ShowBackupSchedule(context.Background()); err == nil {
		t.Fatal("corrupt schedule state was accepted")
	}
	if removed, err := manager.RemoveBackupSchedule(context.Background()); err == nil || removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.reads != readsBefore || runner.writes != writesBefore {
		t.Fatal("corrupt state caused a crontab read or write")
	}
}

func TestBackupScheduleRejectsSymlinkedStateBeforeCrontabAccess(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	external := filepath.Join(t.TempDir(), "external-state.json")
	if err := os.WriteFile(external, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, manager.Paths.Schedule); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ShowBackupSchedule(context.Background()); err == nil || !strings.Contains(err.Error(), "real regular file") {
		t.Fatalf("error = %v", err)
	}
	if runner.reads != 0 || runner.writes != 0 {
		t.Fatal("symlinked schedule state caused crontab access")
	}
}

func TestBackupScheduleMissingStateLeavesCrontabUntouched(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: testExecutable(t, t.TempDir()),
	}); err != nil {
		t.Fatal(err)
	}
	previousCrontab := runner.content
	if err := os.Remove(manager.Paths.Schedule); err != nil {
		t.Fatal(err)
	}
	readsBefore, writesBefore := runner.reads, runner.writes
	if _, found, err := manager.ShowBackupSchedule(context.Background()); err != nil || found {
		t.Fatalf("ShowBackupSchedule: found=%t err=%v", found, err)
	}
	if removed, err := manager.RemoveBackupSchedule(context.Background()); err != nil || removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.reads != readsBefore || runner.writes != writesBefore || runner.content != previousCrontab {
		t.Fatal("missing authoritative state caused an unsafe crontab change")
	}
}

func TestBackupScheduleStateWithoutOwnedBlockFailsSafe(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: testExecutable(t, t.TempDir()),
	}); err != nil {
		t.Fatal(err)
	}
	runner.content = "0 1 * * * /usr/bin/unrelated\n"
	writesBefore := runner.writes
	if _, _, err := manager.ShowBackupSchedule(context.Background()); err == nil {
		t.Fatal("missing owned block was accepted")
	}
	if removed, err := manager.RemoveBackupSchedule(context.Background()); err == nil || removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.writes != writesBefore {
		t.Fatal("missing owned block caused a crontab write")
	}
}

func TestBackupSchedulePreservesMissingTrailingNewline(t *testing.T) {
	initial := "MAILTO=user@example.test"
	runner := &crontabRunner{content: initial, exists: true}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 3 * * *",
		Keep:       5,
		Executable: testExecutable(t, t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := manager.RemoveBackupSchedule(context.Background())
	if err != nil || !removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.content != initial {
		t.Fatalf("crontab after round trip = %q, want %q", runner.content, initial)
	}
}

func TestBackupScheduleRejectsUnsafeCronWithoutWriting(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	executable := testExecutable(t, t.TempDir())
	for _, cron := range []string{
		"0 3 * * *; touch /tmp/unsafe",
		"0 3 * * *\n* * * * *",
		"0  3 * * *",
		"0 3 * *",
		"0 3 * * * %payload",
	} {
		_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{Cron: cron, Keep: 5, Executable: executable})
		if err == nil {
			t.Fatalf("unsafe cron %q was accepted", cron)
		}
	}
	if runner.writes != 0 {
		t.Fatalf("unsafe schedules wrote crontab %d time(s)", runner.writes)
	}
}

func TestBackupScheduleRejectsCronSpecialCharacterInExecutable(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	directory := filepath.Join(t.TempDir(), "percent%dir")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 3 * * *",
		Keep:       5,
		Executable: testExecutable(t, directory),
	})
	if err == nil || !strings.Contains(err.Error(), "crontab") {
		t.Fatalf("error = %v", err)
	}
	if runner.writes != 0 {
		t.Fatal("unsafe executable path wrote crontab")
	}
}

func TestBackupScheduleRejectsUnsafeCapturedPath(t *testing.T) {
	_, err := normalizeBackupSchedule(BackupSchedule{
		Cron: "0 3 * * *", Keep: 5, Executable: "/usr/local/bin/rakazo-manager", InstanceRoot: "/srv/rakazo", Path: "relative:/usr/bin",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "absolute directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestBackupScheduleDoesNotOverwriteUnreadableCrontab(t *testing.T) {
	runner := &crontabRunner{listErr: errors.New("exit status 1"), listText: "permission denied"}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 3 * * *",
		Keep:       5,
		Executable: testExecutable(t, t.TempDir()),
	})
	if err == nil || !strings.Contains(err.Error(), "read current crontab") {
		t.Fatalf("error = %v", err)
	}
	if runner.writes != 0 {
		t.Fatalf("unreadable crontab was overwritten")
	}
}

func TestBackupScheduleRejectsModifiedOwnedBlock(t *testing.T) {
	runner := &crontabRunner{}
	manager, _ := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	_, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 3 * * *",
		Keep:       5,
		Executable: testExecutable(t, t.TempDir()),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.content = "# BEGIN RAKAZO-MANAGER BACKUP test-instance\n"
	writesBefore := runner.writes
	removed, err := manager.RemoveBackupSchedule(context.Background())
	if err == nil || removed {
		t.Fatalf("RemoveBackupSchedule: removed=%t err=%v", removed, err)
	}
	if runner.writes != writesBefore {
		t.Fatalf("malformed owned block caused a write")
	}
}

func TestRunScheduledBackupCreatesRecognizedAutomaticBackupAndPrunes(t *testing.T) {
	runner := &backupCommandRunner{t: t}
	manager, paths := installedTestManager(t, runner, "ghcr.io/elie222/rakazo/app:latest", "")
	for _, directory := range []string{paths.Data, paths.Workspace, paths.Backups} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	oldScheduled := filepath.Join(paths.Backups, "rakazo-scheduled-20200101T000000.000Z.zip")
	manual := filepath.Join(paths.Backups, "rakazo-manual-20200101T000000.000Z.zip")
	preUpdateOlder := filepath.Join(paths.Backups, "rakazo-pre-update-20200101T000000.000Z.zip")
	preUpdateNewer := filepath.Join(paths.Backups, "rakazo-pre-update-20990101T000000.000Z.zip")
	writeTestZip(t, oldScheduled)
	writeTestZip(t, manual)
	writeTestZip(t, preUpdateOlder)
	writeTestZip(t, preUpdateNewer)
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldScheduled, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	olderTime := oldTime.Add(-time.Hour)
	if err := os.Chtimes(preUpdateOlder, olderTime, olderTime); err != nil {
		t.Fatal(err)
	}
	newerTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(preUpdateNewer, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}

	result, err := manager.RunScheduledBackup(context.Background(), 1)
	if err != nil {
		t.Fatalf("RunScheduledBackup: %v", err)
	}
	if !automaticBackupName(filepath.Base(result.Archive)) || len(result.Pruned) != 1 || result.Pruned[0] != filepath.Base(oldScheduled) {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(manual); err != nil {
		t.Fatalf("manual backup was pruned: %v", err)
	}
	for _, path := range []string{preUpdateOlder, preUpdateNewer} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("pre-update safety backup was pruned: %s: %v", filepath.Base(path), err)
		}
	}
}

func TestScheduledBackupLogIsBoundedBeforeRun(t *testing.T) {
	manager, _ := installedTestManager(t, &backupCommandRunner{t: t}, "ghcr.io/elie222/rakazo/app:latest", "")
	if err := os.WriteFile(manager.Paths.ScheduleLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(manager.Paths.ScheduleLog, maxScheduleLogSize+1); err != nil {
		t.Fatal(err)
	}
	if err := manager.rotateScheduleLog(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manager.Paths.ScheduleLog)
	if err != nil || info.Size() != 0 {
		t.Fatalf("bounded schedule log: size=%d err=%v", info.Size(), err)
	}
}

type backupCommandRunner struct {
	t *testing.T
}

func (runner *backupCommandRunner) Run(_ context.Context, request command.Request) (command.Result, error) {
	runner.t.Helper()
	joined := strings.Join(request.Args, " ")
	switch {
	case request.Name == "docker" && strings.Contains(joined, "--version"):
		return command.Result{Stdout: "Docker test"}, nil
	case request.Name == "docker" && strings.Contains(joined, "compose version"):
		return command.Result{Stdout: "Docker Compose test"}, nil
	case request.Name == "docker" && strings.Contains(joined, "info --format"):
		return command.Result{Stdout: "test"}, nil
	case request.Name == "docker" && strings.Contains(joined, "ps --status running --services"):
		return command.Result{Stdout: "api\n"}, nil
	case request.Name == "docker" && strings.Contains(joined, "exec -T postgres pg_dump"):
		return command.Result{Stdout: "DUMP"}, nil
	default:
		return command.Result{}, nil
	}
}

func executableWithSpecialPath(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "bin dir's")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return testExecutable(t, directory)
}

func writeTestZip(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	member, err := archive.Create("backup.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte("backup")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
