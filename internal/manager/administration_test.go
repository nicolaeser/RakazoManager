package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/stack"
	"github.com/nicolaeser/RakazoManager/internal/state"
)

type adminTestRunner struct {
	mu             sync.Mutex
	requests       []command.Request
	running        bool
	exists         bool
	root           string
	upFailures     int
	createFailures int
	crontab        string
	crontabReadErr error
	downFailure    error
	onRestart      func()
}

func (runner *adminTestRunner) Run(_ context.Context, request command.Request) (command.Result, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	request.Args = append([]string(nil), request.Args...)
	runner.requests = append(runner.requests, request)
	if request.Name == "crontab" {
		switch strings.Join(request.Args, " ") {
		case "-l":
			if runner.crontabReadErr != nil {
				return command.Result{Stderr: runner.crontabReadErr.Error()}, runner.crontabReadErr
			}
			return command.Result{Stdout: runner.crontab}, nil
		case "-":
			content, err := io.ReadAll(request.Stdin)
			if err != nil {
				return command.Result{}, err
			}
			runner.crontab = string(content)
			return command.Result{}, nil
		}
	}
	joined := strings.Join(request.Args, " ")
	switch {
	case strings.Contains(joined, "ps --status running --services"):
		if runner.running {
			return command.Result{Stdout: "api\n"}, nil
		}
		return command.Result{}, nil
	case strings.Contains(joined, "ps -a -q api"):
		if runner.exists || runner.running {
			return command.Result{Stdout: "container-id\n"}, nil
		}
		return command.Result{}, nil
	case strings.Contains(joined, "ps -q api") || strings.Contains(joined, "ps -q postgres"):
		if runner.running {
			return command.Result{Stdout: "container-id\n"}, nil
		}
		return command.Result{}, nil
	case len(request.Args) > 0 && request.Args[0] == "inspect" && strings.Contains(joined, "{{json .Mounts}}"):
		return command.Result{Stdout: fmt.Sprintf(
			`[{"Type":"bind","Source":%q,"Destination":"/data"},{"Type":"bind","Source":%q,"Destination":"/backups"},{"Type":"bind","Source":%q,"Destination":"/var/lib/postgresql/data"}]`,
			filepath.Join(runner.root, "data"), filepath.Join(runner.root, "backups"), filepath.Join(runner.root, "pg"),
		)}, nil
	case strings.Contains(joined, "up --no-start") || strings.Contains(joined, " create "):
		if runner.createFailures > 0 {
			runner.createFailures--
			return command.Result{}, errors.New("injected compose create failure")
		}
		runner.exists = true
		return command.Result{}, nil
	case strings.Contains(joined, " up ") || strings.HasSuffix(joined, " up") || strings.Contains(joined, " up -d"):
		if runner.upFailures > 0 {
			runner.upFailures--
			return command.Result{}, errors.New("injected compose up failure")
		}
		runner.running = true
		runner.exists = true
		return command.Result{}, nil
	case strings.Contains(joined, " restart "):
		if runner.onRestart != nil {
			runner.onRestart()
			runner.onRestart = nil
		}
		return command.Result{}, nil
	case strings.Contains(joined, " down "):
		if runner.downFailure != nil {
			return command.Result{}, runner.downFailure
		}
		runner.running = false
		runner.exists = false
		return command.Result{}, nil
	default:
		return command.Result{}, nil
	}
}

func (runner *adminTestRunner) countRequests(fragment string) int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	count := 0
	for _, request := range runner.requests {
		if strings.Contains(request.Name+" "+strings.Join(request.Args, " "), fragment) {
			count++
		}
	}
	return count
}

func newInstalledAdminManager(t *testing.T, runner *adminTestRunner, dashboardPort, apiPort int) (*Manager, config.Config) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "instance")
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	runner.root = root
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	manager.DashboardWait = func(context.Context, time.Duration) (DashboardHealth, error) {
		return DashboardHealth{OK: true, Version: "test", AuthRequired: true}, nil
	}
	cfg := config.New(root, "rakazo-admin-test", config.DefaultImage, dashboardPort, apiPort, freeAdminPort(t))
	if err := manager.ConfigStore.Save(cfg); err != nil {
		t.Fatal(err)
	}
	values, _, err := manager.SecretStore.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Generator.Prepare(cfg, values, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.StateStore.Save(state.State{SchemaVersion: state.SchemaVersion}); err != nil {
		t.Fatal(err)
	}
	return manager, cfg
}

var nextAdminPort atomic.Int64

func freeAdminPort(t *testing.T) int {
	t.Helper()
	return 40000 + int(nextAdminPort.Add(1))
}

func TestDiscoverInstancesReportsInvalidImmediateChildren(t *testing.T) {
	parent := t.TempDir()
	validRoot := filepath.Join(parent, "valid")
	validPaths, _ := stack.NewPaths(validRoot)
	validCfg := config.New(validRoot, "rakazo-valid", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := (config.Store{Paths: validPaths}).Save(validCfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validPaths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalidRoot := filepath.Join(parent, "invalid")
	if err := os.MkdirAll(filepath.Join(invalidRoot, ".manager"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidRoot, ".manager", "instance.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(parent, "symlink-compose")
	symlinkPaths, _ := stack.NewPaths(symlinkRoot)
	symlinkCfg := config.New(symlinkRoot, "rakazo-symlink-compose", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := (config.Store{Paths: symlinkPaths}).Save(symlinkCfg); err != nil {
		t.Fatal(err)
	}
	externalCompose := filepath.Join(t.TempDir(), "external-compose.yml")
	if err := os.WriteFile(externalCompose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalCompose, symlinkPaths.Compose); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(parent, "unrelated", "nested", "instance")
	unrelatedPaths, _ := stack.NewPaths(unrelated)
	if err := (config.Store{Paths: unrelatedPaths}).Save(config.New(unrelated, "rakazo-nested", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelatedPaths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := DiscoverInstances(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instances) != 1 || result.Instances[0].Root != validPaths.Root {
		t.Fatalf("unexpected valid instances: %#v", result.Instances)
	}
	if len(result.Invalid) != 2 || result.Invalid[0].Root != invalidRoot || result.Invalid[1].Root != symlinkRoot || !strings.Contains(result.Invalid[1].Reason, "not a real regular file") {
		t.Fatalf("unexpected invalid instances: %#v", result.Invalid)
	}
}

func TestInstallAndPreviewRejectDuplicateSiblingName(t *testing.T) {
	parent := t.TempDir()
	siblingRoot := filepath.Join(parent, "sibling")
	siblingPaths, _ := stack.NewPaths(siblingRoot)
	duplicateName := "rakazo-duplicate-name"
	siblingConfig := config.New(siblingRoot, duplicateName, config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := (config.Store{Paths: siblingPaths}).Save(siblingConfig); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(parent, "target")
	paths, _ := stack.NewPaths(target)
	runner := &adminTestRunner{root: target}
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	options := InstallOptions{Name: duplicateName, WebPort: freeAdminPort(t), APIPort: freeAdminPort(t)}
	if _, err := manager.PreviewInstall(options); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("expected duplicate-name preview rejection, got %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created target: %v", err)
	}
	if _, err := manager.Install(context.Background(), options); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("expected duplicate-name install rejection, got %v", err)
	}
	if _, err := os.Lstat(paths.Config); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("duplicate-name install wrote metadata: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("duplicate-name install invoked Docker: %#v", runner.requests)
	}
}

func TestPlanConfigPatchIsReadOnlyAndClearsImagePin(t *testing.T) {
	runner := &adminTestRunner{}
	manager, cfg := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	cfg.PinnedImage = "ghcr.io/elie222/rakazo/app@sha256:abc"
	if err := manager.ConfigStore.Save(cfg); err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/elie222/rakazo/app:v2"
	name := "rakazo-renamed"
	plan, err := manager.PlanConfigPatch(ConfigPatch{Name: &name, Image: &image})
	if err != nil {
		t.Fatal(err)
	}
	if plan.After.PinnedImage != "" || !plan.RequiresRecreate || len(plan.Changes) != 3 {
		t.Fatalf("unexpected config plan: %#v", plan)
	}
	loaded, err := manager.ConfigStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != cfg.Name || loaded.PinnedImage != cfg.PinnedImage {
		t.Fatalf("planning mutated metadata: %#v", loaded)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("planning unexpectedly invoked commands: %#v", runner.requests)
	}
}

func TestApplyConfigPatchRecreatesRunningService(t *testing.T) {
	runner := &adminTestRunner{running: true}
	dashboardPort := freeAdminPort(t)
	manager, cfg := newInstalledAdminManager(t, runner, dashboardPort, freeAdminPort(t))
	name := "rakazo-configured"
	image := "ghcr.io/elie222/rakazo/app:v2"
	plan, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Name: &name, Image: &image}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresRecreate || runner.countRequests("up -d --wait") != 1 {
		t.Fatalf("running service was not recreated: %#v", runner.requests)
	}
	loaded, _ := manager.ConfigStore.Load()
	if loaded.Name != name || loaded.Image != image || loaded.PinnedImage != "" {
		t.Fatalf("unexpected applied config: %#v", loaded)
	}
	composeBytes, _ := os.ReadFile(manager.Paths.Compose)
	if !strings.Contains(string(composeBytes), name) || strings.Contains(string(composeBytes), cfg.Name) {
		t.Fatalf("managed Compose fields were not updated:\n%s", composeBytes)
	}
}

func TestApplyConfigPatchRejectsNameChangeWhileBackupScheduleExists(t *testing.T) {
	runner := &adminTestRunner{running: true}
	manager, cfg := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	executable := filepath.Join(t.TempDir(), "rakazo-manager")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 3 * * *", Keep: 7, Executable: executable,
	}); err != nil {
		t.Fatal(err)
	}
	name := "rakazo-renamed-with-schedule"
	_, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Name: &name}, false)
	if err == nil || !strings.Contains(err.Error(), "run schedule remove first") {
		t.Fatalf("error = %v", err)
	}
	loaded, loadErr := manager.ConfigStore.Load()
	if loadErr != nil || loaded.Name != cfg.Name {
		t.Fatalf("name changed despite schedule: cfg=%#v err=%v", loaded, loadErr)
	}
}

func TestApplyConfigPatchRollsBackMetadataComposeAndContainer(t *testing.T) {
	runner := &adminTestRunner{running: true, upFailures: 1}
	dashboardPort := freeAdminPort(t)
	manager, cfg := newInstalledAdminManager(t, runner, dashboardPort, freeAdminPort(t))
	name := "rakazo-change-that-fails"
	_, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Name: &name}, false)
	if err == nil || !strings.Contains(err.Error(), "previous configuration and container behavior were restored") {
		t.Fatalf("expected successful rollback error, got %v", err)
	}
	loaded, loadErr := manager.ConfigStore.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded != cfg {
		t.Fatalf("metadata was not rolled back: got %#v want %#v", loaded, cfg)
	}
	composeBytes, _ := os.ReadFile(manager.Paths.Compose)
	if !strings.Contains(string(composeBytes), cfg.Name) || strings.Contains(string(composeBytes), name) {
		t.Fatalf("Compose was not rolled back:\n%s", composeBytes)
	}
	if runner.countRequests("up -d --wait") < 1 {
		t.Fatalf("expected failed apply plus rollback recreate: %#v", runner.requests)
	}
}

func TestApplyConfigPatchRollbackRestoresLegacyComposePathExactly(t *testing.T) {
	runner := &adminTestRunner{running: true, upFailures: 1}
	manager, before := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	legacyContent := []byte("# retained operator comment\nservices:\n  api:\n    image: \"ghcr.io/elie222/rakazo/app:latest\"\n    container_name: \"" + before.Name + "\"\n")
	if err := os.Remove(manager.Paths.Compose); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.Paths.LegacyCompose(), legacyContent, 0o640); err != nil {
		t.Fatal(err)
	}
	name := "rakazo-legacy-change"
	_, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Name: &name}, false)
	if err == nil {
		t.Fatal("expected injected recreate failure")
	}
	if _, statErr := os.Lstat(manager.Paths.Compose); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rollback left migrated Compose path behind: %v", statErr)
	}
	got, readErr := os.ReadFile(manager.Paths.LegacyCompose())
	if readErr != nil || string(got) != string(legacyContent) {
		t.Fatalf("legacy Compose was not restored exactly: content=%q err=%v", got, readErr)
	}
	info, statErr := os.Stat(manager.Paths.LegacyCompose())
	if statErr != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("legacy Compose mode was not restored: info=%v err=%v", info, statErr)
	}
}

func TestApplyConfigPatchRecreatesStoppedInstanceWithoutStarting(t *testing.T) {
	runner := &adminTestRunner{running: false, exists: true}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	port := freeAdminPort(t)
	plan, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{WebPort: &port}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresRecreate || runner.countRequests("up --no-start --force-recreate") != 1 || runner.countRequests("up -d") != 0 {
		t.Fatalf("stopped service should be recreated without starting: %#v", runner.requests)
	}
	loaded, _ := manager.ConfigStore.Load()
	if loaded.WebPort != port {
		t.Fatalf("web port was not saved: %#v", loaded)
	}
}

func TestApplyConfigPatchRollsBackStoppedContainerRecreate(t *testing.T) {
	runner := &adminTestRunner{exists: true, createFailures: 1}
	manager, before := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	name := "rakazo-stopped-change"
	_, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Name: &name}, false)
	if err == nil || !strings.Contains(err.Error(), "previous configuration and container behavior were restored") {
		t.Fatalf("expected stopped-container rollback, got %v", err)
	}
	after, loadErr := manager.ConfigStore.Load()
	if loadErr != nil || after != before {
		t.Fatalf("stopped rollback did not restore metadata: got=%#v err=%v", after, loadErr)
	}
	if runner.countRequests("up --no-start --force-recreate") < 1 || runner.countRequests("up -d") != 0 {
		t.Fatalf("stopped rollback changed running state: %#v", runner.requests)
	}
}

func TestApplyConfigPatchDryRunRejectsReservedChangedPortWithoutMutation(t *testing.T) {
	runner := &adminTestRunner{running: false}
	manager, before := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	reserved := freeAdminPort(t)
	siblingRoot := filepath.Join(filepath.Dir(manager.Paths.Root), "sibling")
	siblingPaths, _ := stack.NewPaths(siblingRoot)
	siblingConfig := config.New(siblingRoot, "rakazo-sibling", config.DefaultImage, reserved, freeAdminPort(t), freeAdminPort(t))
	if err := (config.Store{Paths: siblingPaths}).Save(siblingConfig); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingPaths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PlanConfigPatch(ConfigPatch{WebPort: &reserved}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected read-only plan to reject reserved port, got %v", err)
	}
	_, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{WebPort: &reserved}, true)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved-port rejection, got %v", err)
	}
	after, _ := manager.ConfigStore.Load()
	if after != before {
		t.Fatalf("dry run mutated metadata: got %#v want %#v", after, before)
	}
}

func TestApplyConfigPatchDryRunReportsRuntimeAction(t *testing.T) {
	tests := []struct {
		name        string
		running     bool
		exists      bool
		wantAction  string
		wantExists  bool
		wantRunning bool
	}{
		{"running service", true, true, "recreate-running-service-and-verify", true, true},
		{"stopped service", false, true, "recreate-stopped-service", true, false},
		{"no service", false, false, "update-compose-only", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &adminTestRunner{running: test.running, exists: test.exists}
			manager, before := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
			image := "ghcr.io/elie222/rakazo/app:v2"
			plan, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Image: &image}, true)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.RuntimeInspected || plan.RuntimeAction != test.wantAction || plan.ServiceExists != test.wantExists || plan.ServiceRunning != test.wantRunning {
				t.Fatalf("unexpected dry-run runtime facts: %#v", plan)
			}
			after, err := manager.ConfigStore.Load()
			if err != nil || after != before {
				t.Fatalf("dry run changed metadata: cfg=%#v err=%v", after, err)
			}
			if runner.countRequests(" up ") != 0 || runner.countRequests(" create ") != 0 {
				t.Fatalf("dry run changed runtime: %#v", runner.requests)
			}
		})
	}
}

func TestDecommissionRetainsDurableAndUnknownFiles(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	for _, path := range []string{manager.Paths.Data, manager.Paths.Workspace, manager.Paths.Backups} {
		if err := os.WriteFile(filepath.Join(path, "sentinel"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(manager.Paths.ComposeOverride(), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scheduleLog := manager.Paths.ScheduleLog
	unknown := filepath.Join(manager.Paths.Manager, "operator-note")
	if err := os.WriteFile(scheduleLog, []byte("generated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unknown, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := manager.Decommission(context.Background(), DecommissionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DeletesDurableData || runner.countRequests("down --remove-orphans") != 1 || runner.countRequests("down -v") != 0 {
		t.Fatalf("unsafe or missing decommission action: plan=%#v requests=%#v", plan, runner.requests)
	}
	for _, path := range []string{manager.Paths.Data, manager.Paths.Workspace, manager.Paths.Backups, manager.Paths.ComposeOverride(), unknown} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("retained path %s is missing: %v", path, err)
		}
	}
	for _, path := range []string{manager.Paths.Compose, manager.Paths.Config, manager.Paths.Secrets, manager.Paths.State, scheduleLog} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("generated path %s remains: %v", path, err)
		}
	}
}

func TestDecommissionRequiresGuardAndDoesNotFollowDurableSymlink(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	if _, err := manager.Decommission(context.Background(), DecommissionOptions{DeleteData: true}); err == nil {
		t.Fatal("expected explicit deletion guard rejection")
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(manager.Paths.Data); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, manager.Paths.Data); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Decommission(context.Background(), DecommissionOptions{DeleteData: true, AllowDataDeletion: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(manager.Paths.Data); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected symlink was not removed: %v", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "keep" {
		t.Fatalf("symlink target was modified: content=%q err=%v", content, err)
	}
}

func TestDecommissionScheduleFailurePreventsContainerAndFileRemoval(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	executable := filepath.Join(t.TempDir(), "rakazo-manager")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 2 * * *", Keep: 7, Executable: executable,
	}); err != nil {
		t.Fatal(err)
	}
	runner.crontabReadErr = errors.New("permission denied")
	_, err := manager.Decommission(context.Background(), DecommissionOptions{})
	if err == nil || !strings.Contains(err.Error(), "owned backup schedule") {
		t.Fatalf("expected schedule removal error, got %v", err)
	}
	if runner.countRequests("down --remove-orphans") != 0 {
		t.Fatal("containers were removed after schedule removal failed")
	}
	if _, err := os.Stat(manager.Paths.Config); err != nil {
		t.Fatalf("metadata was removed after schedule failure: %v", err)
	}
}

func TestDecommissionRestoresScheduleWhenComposeDownFails(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	executable := filepath.Join(t.TempDir(), "rakazo-manager")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	want, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron:       "0 2 * * *",
		Keep:       7,
		Executable: executable,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.downFailure = errors.New("injected compose down failure")
	plan, err := manager.Decommission(context.Background(), DecommissionOptions{})
	if err == nil || !strings.Contains(err.Error(), "owned backup schedule was restored") {
		t.Fatalf("expected restored-schedule error, got %v", err)
	}
	if !plan.RemoveBackupSchedule {
		t.Fatalf("decommission plan did not report the configured schedule: %#v", plan)
	}
	got, found, showErr := manager.ShowBackupSchedule(context.Background())
	if showErr != nil || !found || got != want {
		t.Fatalf("schedule was not restored: got=%#v found=%t err=%v", got, found, showErr)
	}
	if _, statErr := os.Stat(manager.Paths.Config); statErr != nil {
		t.Fatalf("metadata was removed after down failure: %v", statErr)
	}
}

func TestShowConfigDoesNotRegenerateMissingSecrets(t *testing.T) {
	runner := &adminTestRunner{}
	manager, cfg := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	if err := os.Remove(manager.Paths.Secrets); err != nil {
		t.Fatal(err)
	}
	shown, err := manager.ShowConfig()
	if err != nil {
		t.Fatal(err)
	}
	if shown != cfg {
		t.Fatalf("unexpected config: %#v", shown)
	}
	if _, err := os.Stat(manager.Paths.Secrets); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only config regenerated secrets: %v", err)
	}
}

func TestDecommissionPlanIncludesScheduleLogAsGenerated(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	scheduleLog := manager.Paths.ScheduleLog
	if err := os.WriteFile(scheduleLog, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := manager.PlanDecommission(DecommissionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range plan.Remove {
		found = found || path == scheduleLog
	}
	if !found || plan.RemoveBackupSchedule {
		t.Fatalf("schedule-log-only plan reported incorrect cleanup: %#v", plan)
	}
}

func TestDecommissionRefusesSymlinkedManagerDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "instance")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	externalManager := filepath.Join(parent, "external-manager")
	if err := os.Mkdir(externalManager, 0o700); err != nil {
		t.Fatal(err)
	}
	externalPaths := paths
	externalPaths.Manager = externalManager
	externalPaths.Config = filepath.Join(externalManager, "instance.json")
	externalPaths.Secrets = filepath.Join(externalManager, "secrets.env")
	externalPaths.State = filepath.Join(externalManager, "state.json")
	externalPaths.OperationsLog = filepath.Join(externalManager, "operations.log")
	externalPaths.Lock = filepath.Join(externalManager, "operation.lock")
	cfg := config.New(root, "rakazo-symlink-guard", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := (config.Store{Paths: externalPaths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalManager, paths.Manager); err != nil {
		t.Fatal(err)
	}
	runner := &adminTestRunner{root: root}
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	if _, err := manager.PlanDecommission(DecommissionOptions{}); err == nil || !strings.Contains(err.Error(), "manager directory is not a real directory") {
		t.Fatalf("expected symlinked manager refusal, got %v", err)
	}
	if _, err := os.Stat(externalPaths.Config); err != nil {
		t.Fatalf("external manager metadata was modified: %v", err)
	}
	if _, err := os.Stat(paths.Compose); err != nil {
		t.Fatalf("instance Compose was modified: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("decommission invoked commands before layout validation: %#v", runner.requests)
	}
}

func TestConfigOperationsRefuseSymlinkedManagerDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "instance")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	paths, _ := stack.NewPaths(root)
	externalManager := filepath.Join(parent, "external-manager")
	externalPaths := paths
	externalPaths.Manager = externalManager
	externalPaths.Config = filepath.Join(externalManager, "instance.json")
	externalPaths.Secrets = filepath.Join(externalManager, "secrets.env")
	externalPaths.State = filepath.Join(externalManager, "state.json")
	externalPaths.OperationsLog = filepath.Join(externalManager, "operations.log")
	externalPaths.Lock = filepath.Join(externalManager, "operation.lock")
	cfg := config.New(root, "rakazo-config-symlink", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := (config.Store{Paths: externalPaths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalManager, paths.Manager); err != nil {
		t.Fatal(err)
	}
	runner := &adminTestRunner{root: root}
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	if _, err := manager.ShowConfig(); err == nil || !strings.Contains(err.Error(), "manager directory is not a real directory") {
		t.Fatalf("expected config-show symlink refusal, got %v", err)
	}
	name := "rakazo-should-not-be-written"
	if _, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Name: &name}, false); err == nil || !strings.Contains(err.Error(), "manager directory is not a real directory") {
		t.Fatalf("expected config-set symlink refusal, got %v", err)
	}
	loaded, err := (config.Store{Paths: externalPaths}).Load()
	if err != nil || loaded != cfg {
		t.Fatalf("external metadata changed: cfg=%#v err=%v", loaded, err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("unsafe config path invoked commands: %#v", runner.requests)
	}
}

func TestApplyConfigPatchRefusesSymlinkedCredentials(t *testing.T) {
	runner := &adminTestRunner{}
	manager, before := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	external := filepath.Join(t.TempDir(), "external-secrets.env")
	wantExternal := []byte("do-not-replace\n")
	if err := os.WriteFile(external, wantExternal, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manager.Paths.Secrets); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, manager.Paths.Secrets); err != nil {
		t.Fatal(err)
	}
	image := "ghcr.io/elie222/rakazo/app:v2"
	if _, err := manager.PlanConfigPatch(ConfigPatch{Image: &image}); err == nil || !strings.Contains(err.Error(), "instance secrets is not a real regular file") {
		t.Fatalf("expected pure plan credentials symlink refusal, got %v", err)
	}
	if _, err := manager.ApplyConfigPatch(context.Background(), ConfigPatch{Image: &image}, false); err == nil || !strings.Contains(err.Error(), "instance secrets is not a real regular file") {
		t.Fatalf("expected credentials symlink refusal, got %v", err)
	}
	after, err := manager.ConfigStore.Load()
	if err != nil || after != before {
		t.Fatalf("metadata changed after refusal: cfg=%#v err=%v", after, err)
	}
	gotExternal, err := os.ReadFile(external)
	if err != nil || string(gotExternal) != string(wantExternal) {
		t.Fatalf("external credentials changed: content=%q err=%v", gotExternal, err)
	}
}

func TestDecommissionDoesNotRestoreScheduleAfterPartialFileRemoval(t *testing.T) {
	runner := &adminTestRunner{}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	executable := filepath.Join(t.TempDir(), "rakazo-manager")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetBackupSchedule(context.Background(), BackupSchedule{
		Cron: "0 2 * * *", Keep: 7, Executable: executable,
	}); err != nil {
		t.Fatal(err)
	}
	scheduleLog := manager.Paths.ScheduleLog
	if err := os.Mkdir(scheduleLog, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Decommission(context.Background(), DecommissionOptions{})
	if err == nil || !strings.Contains(err.Error(), "schedule remains removed because decommission partially completed") {
		t.Fatalf("expected partial-decommission schedule result, got %v", err)
	}
	if _, found, showErr := manager.ShowBackupSchedule(context.Background()); showErr != nil || found {
		t.Fatalf("schedule was unsafely restored after partial cleanup: found=%t err=%v", found, showErr)
	}
	if _, statErr := os.Stat(manager.Paths.Config); statErr != nil {
		t.Fatalf("metadata should remain for manual recovery: %v", statErr)
	}
}
