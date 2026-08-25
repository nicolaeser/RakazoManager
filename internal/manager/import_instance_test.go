package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

type exportFixtureEntry struct {
	name string
	data []byte
	mode fs.FileMode
}

func writeExportFixture(t *testing.T, archivePath string, cfg config.Config, includeWorkspace bool, embeddedBackup []byte, extras []exportFixtureEntry, mutateManifest func(*InstanceExportManifest)) {
	t.Helper()
	manifest := InstanceExportManifest{
		SchemaVersion:     instanceExportSchema,
		CreatedAt:         time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		InstanceName:      cfg.Name,
		TrackedImage:      cfg.Image,
		RunningImage:      "ghcr.io/elie222/rakazo/app@sha256:abc",
		RakazoBackup:      "rakazo-backup.zip",
		IncludesWorkspace: includeWorkspace,
		ContainsSecrets:   true,
	}
	if mutateManifest != nil {
		mutateManifest(&manifest)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	configBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	entries := []exportFixtureEntry{
		{"manifest.json", manifestBytes, 0o600},
		{"RECOVERY.txt", []byte("recovery fixture"), 0o600},
		{".manager/instance.json", configBytes, 0o600},
		{".manager/secrets.env", []byte(strings.Join([]string{
			secrets.PostgresPassword + "=fixture-password",
			secrets.BetterAuthSecret + "=fixture-auth-secret-at-least-32-chars",
			secrets.EncryptionKey + "=fixture-encryption-key-at-least-32-ch",
			secrets.SupervisorToken + "=fixture-supervisor-token-at-least-32",
			"",
		}, "\n")), 0o600},
		{"generated/docker-compose.yml", []byte("services:\n  api:\n    volumes:\n      - /:/host\n"), 0o600},
		{"rakazo/rakazo-backup.zip", embeddedBackup, 0o600},
	}
	if includeWorkspace {
		entries = append(entries,
			exportFixtureEntry{"workspace/", nil, fs.ModeDir | 0o755},
			exportFixtureEntry{"workspace/notes.txt", []byte("workspace restored"), 0o640},
		)
	}
	skipSecrets := false
	for _, extra := range extras {
		if extra.name == ".manager/secrets.env" {
			skipSecrets = true
			break
		}
	}
	if skipSecrets {
		filtered := entries[:0]
		for _, entry := range entries {
			if entry.name == ".manager/secrets.env" {
				continue
			}
			filtered = append(filtered, entry)
		}
		entries = filtered
	}
	entries = append(entries, extras...)
	for _, entry := range entries {
		var addErr error
		if strings.HasPrefix(entry.name, "rakazo/") && strings.HasSuffix(strings.ToLower(entry.name), ".zip") {
			header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
			header.SetMode(entry.mode)
			member, createErr := writer.CreateHeader(header)
			if createErr != nil {
				addErr = createErr
			} else {
				_, addErr = member.Write(entry.data)
			}
		} else {
			addErr = addBytesToZip(writer, entry.name, entry.data, entry.mode)
		}
		if addErr != nil {
			_ = output.Close()
			t.Fatal(addErr)
		}
	}
	if err := writer.Close(); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func validEmbeddedBackup(t *testing.T) []byte {
	return embeddedBackupWithPayload(t, `{"fixture":true}`)
}

func embeddedBackupWithPayload(t *testing.T, payload string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	if err := addBytesToZip(writer, "backup.json", []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addBytesToZip(writer, "postgres.dump", []byte("DUMP"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func newImportTestManager(t *testing.T, root string, runner *adminTestRunner) *Manager {
	t.Helper()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	runner.root = paths.Root
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	manager.DashboardWait = func(context.Context, time.Duration) (DashboardHealth, error) {
		return DashboardHealth{OK: true, Version: "fixture", AuthRequired: true}, nil
	}
	return manager
}

func fixtureImportConfig(t *testing.T, root string) config.Config {
	t.Helper()
	return config.New(root, "rakazo-import-fixture", "ghcr.io/elie222/rakazo/app:v1", freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
}

func TestImportInstanceDryRunValidatesWithoutCreatingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "recovered")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, true, validEmbeddedBackup(t), nil, nil)
	runner := &adminTestRunner{}
	manager := newImportTestManager(t, target, runner)

	result, err := manager.ImportInstance(context.Background(), archivePath, ImportInstanceOptions{DryRun: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.Plan.Target != target || !result.Plan.IncludesWorkspace {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run created target: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("dry run invoked commands: %#v", runner.requests)
	}
}

func TestImportInstanceWithoutStartStagesRecoverableInstance(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "recovered")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, true, validEmbeddedBackup(t), nil, nil)
	runner := &adminTestRunner{}
	manager := newImportTestManager(t, target, runner)

	result, err := manager.ImportInstance(context.Background(), archivePath, ImportInstanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Started || result.Restored {
		t.Fatalf("unexpected staged import result: %#v", result)
	}
	loaded, err := manager.ConfigStore.Load()
	if err != nil || loaded != cfg {
		t.Fatalf("imported config mismatch: got=%#v err=%v", loaded, err)
	}
	composeBytes, err := os.ReadFile(manager.Paths.Compose)
	if err != nil {
		t.Fatal(err)
	}
	composeText := string(composeBytes)
	if strings.Contains(composeText, "/:/host") || !strings.Contains(composeText, cfg.Name) || !strings.Contains(composeText, "./data:/data") {
		t.Fatalf("archived Compose was trusted or generated Compose is incomplete:\n%s", composeText)
	}
	workspaceBytes, err := os.ReadFile(filepath.Join(manager.Paths.Workspace, "notes.txt"))
	if err != nil || string(workspaceBytes) != "workspace restored" {
		t.Fatalf("workspace was not restored: content=%q err=%v", workspaceBytes, err)
	}
	if err := ValidateZIP(result.Plan.EmbeddedBackup); err != nil {
		t.Fatalf("embedded backup not staged: %v", err)
	}
	secretInfo, err := os.Stat(manager.Paths.Secrets)
	if err != nil || secretInfo.Mode().Perm() != 0o600 {
		t.Fatalf("imported secrets mode is not private: info=%v err=%v", secretInfo, err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("no-start import invoked Docker: %#v", runner.requests)
	}
}

func TestImportInstanceStartsImportsRestartsAndVerifies(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "recovered")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, nil)
	runner := &adminTestRunner{}
	manager := newImportTestManager(t, target, runner)

	result, err := manager.ImportInstance(context.Background(), archivePath, ImportInstanceOptions{Pull: true, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.Started || !result.Restored {
		t.Fatalf("recovery did not complete: %#v", result)
	}
	for _, request := range []string{
		"pull",
		"up -d --wait",
	} {
		if runner.countRequests(request) < 1 {
			t.Fatalf("expected %q request, got %#v", request, runner.requests)
		}
	}
	if runner.countRequests("inspect --format {{json .Mounts}} container-id") < 2 {
		t.Fatalf("expected bind verification before and after import: %#v", runner.requests)
	}
	managerState, err := manager.StateStore.Load()
	if err != nil || managerState.LastOperation != "import-instance" || managerState.LastBackup != "rakazo-backup.zip" {
		t.Fatalf("unexpected imported state: %#v err=%v", managerState, err)
	}
}

func TestImportInstanceRejectsUnsafeArchiveMembers(t *testing.T) {
	tests := []struct {
		name  string
		extra []exportFixtureEntry
		want  string
	}{
		{"traversal", []exportFixtureEntry{{"../escape", []byte("x"), 0o600}}, "unsafe ZIP member"},
		{"absolute", []exportFixtureEntry{{"/absolute", []byte("x"), 0o600}}, "unsafe ZIP member"},
		{"duplicate", []exportFixtureEntry{{"manifest.json", []byte(`{"schema_version":1}`), 0o600}}, "duplicate member"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "target")
			archivePath := filepath.Join(parent, "export.zip")
			cfg := fixtureImportConfig(t, target)
			writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), test.extra, nil)
			manager := newImportTestManager(t, target, &adminTestRunner{})
			_, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q rejection, got %v", test.want, err)
			}
			if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("unsafe archive created target: %v", statErr)
			}
		})
	}
}

func TestImportInstanceRejectsUnsafeWorkspaceSymlinks(t *testing.T) {
	tests := []struct {
		name  string
		extra []exportFixtureEntry
		want  string
	}{
		{"escape", []exportFixtureEntry{{"workspace/link", []byte("../../outside"), os.ModeSymlink | 0o777}}, "escapes"},
		{"terminal control", []exportFixtureEntry{{"workspace/link", []byte("notes.txt\x1b[2J"), os.ModeSymlink | 0o777}}, "unsafe target"},
		{"nested", []exportFixtureEntry{
			{"workspace/link", []byte("."), os.ModeSymlink | 0o777},
			{"workspace/link/nested.txt", []byte("x"), 0o600},
		}, "non-directory ancestor"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "target")
			archivePath := filepath.Join(parent, "export.zip")
			cfg := fixtureImportConfig(t, target)
			writeExportFixture(t, archivePath, cfg, true, validEmbeddedBackup(t), test.extra, nil)
			manager := newImportTestManager(t, target, &adminTestRunner{})
			_, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q rejection, got %v", test.want, err)
			}
		})
	}
}

func TestImportInstanceRejectsInvalidWorkspaceMemberGraph(t *testing.T) {
	t.Run("missing workspace root", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		archivePath := filepath.Join(parent, "export.zip")
		cfg := fixtureImportConfig(t, target)
		writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), []exportFixtureEntry{
			{"workspace/orphan.txt", []byte("x"), 0o600},
		}, func(manifest *InstanceExportManifest) { manifest.IncludesWorkspace = true })
		manager := newImportTestManager(t, target, &adminTestRunner{})
		_, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{})
		if err == nil || !strings.Contains(err.Error(), "workspace/ directory is missing") {
			t.Fatalf("expected missing workspace root rejection, got %v", err)
		}
	})

	t.Run("regular file ancestor", func(t *testing.T) {
		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		archivePath := filepath.Join(parent, "export.zip")
		cfg := fixtureImportConfig(t, target)
		writeExportFixture(t, archivePath, cfg, true, validEmbeddedBackup(t), []exportFixtureEntry{
			{"workspace/file", []byte("x"), 0o600},
			{"workspace/file/nested.txt", []byte("y"), 0o600},
		}, nil)
		manager := newImportTestManager(t, target, &adminTestRunner{})
		_, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{})
		if err == nil || !strings.Contains(err.Error(), "non-directory ancestor") {
			t.Fatalf("expected non-directory ancestor rejection, got %v", err)
		}
	})
}

func TestImportInstanceRejectsManifestConfigMismatch(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, func(manifest *InstanceExportManifest) {
		manifest.TrackedImage = "example.invalid/different:v9"
	})
	manager := newImportTestManager(t, target, &adminTestRunner{})
	_, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected manifest mismatch, got %v", err)
	}
}

func TestImportInstanceRejectsNonemptyAndSymlinkTargets(t *testing.T) {
	parent := t.TempDir()
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, filepath.Join(parent, "unused"))
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, nil)

	t.Run("nonempty", func(t *testing.T) {
		target := filepath.Join(parent, "nonempty")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		manager := newImportTestManager(t, target, &adminTestRunner{})
		if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "not empty") {
			t.Fatalf("expected nonempty target rejection, got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		realTarget := filepath.Join(parent, "real")
		if err := os.Mkdir(realTarget, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, "link")
		if err := os.Symlink(realTarget, target); err != nil {
			t.Fatal(err)
		}
		manager := newImportTestManager(t, target, &adminTestRunner{})
		if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("expected symlink target rejection, got %v", err)
		}
	})
}

func TestImportInstanceConflictsRequireOverrides(t *testing.T) {
	parent := t.TempDir()
	sourceRoot := filepath.Join(parent, "source")
	target := filepath.Join(parent, "target")
	cfg := fixtureImportConfig(t, sourceRoot)
	sourcePaths, _ := stack.NewPaths(sourceRoot)
	if err := (config.Store{Paths: sourcePaths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePaths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(parent, "export.zip")
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, nil)
	manager := newImportTestManager(t, target, &adminTestRunner{})
	if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected sibling name conflict, got %v", err)
	}
	newName := "rakazo-recovered"
	if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{Patch: ConfigPatch{Name: &newName}}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected sibling port conflict, got %v", err)
	}
	dashboardPort, apiPort, postgresPort := freeAdminPort(t), freeAdminPort(t), freeAdminPort(t)
	image := "ghcr.io/elie222/rakazo/app:v2"
	plan, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{Patch: ConfigPatch{
		Name: &newName, Image: &image, WebPort: &dashboardPort, APIPort: &apiPort, PostgresPort: &postgresPort,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Config.Name != newName || plan.Config.Image != image || plan.Config.WebPort != dashboardPort || plan.Config.APIPort != apiPort {
		t.Fatalf("overrides not reflected in import plan: %#v", plan.Config)
	}
}

func TestImportInstanceFailureBeforePublishLeavesTargetUntouched(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, []byte("not a nested zip"), nil, nil)
	manager := newImportTestManager(t, target, &adminTestRunner{})
	result, err := manager.ImportInstance(context.Background(), archivePath, ImportInstanceOptions{})
	if err == nil || !strings.Contains(err.Error(), "embedded Rakazo backup") || result.Applied {
		t.Fatalf("expected pre-publish embedded-backup failure, result=%#v err=%v", result, err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed staging published target: %v", statErr)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".rakazo-import-") {
			t.Fatalf("failed staging directory was not recovered: %s", entry.Name())
		}
	}
}

func TestPlanImportInstanceValidatesEmbeddedBackupWithoutMutation(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, []byte("not a nested ZIP"), nil, nil)
	runner := &adminTestRunner{}
	manager := newImportTestManager(t, target, runner)
	if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "embedded Rakazo backup") {
		t.Fatalf("expected nested-backup validation failure, got %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import plan created target: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("import plan invoked commands: %#v", runner.requests)
	}
}

func TestImportInstanceRejectsUnsafeArchivedManagerHistory(t *testing.T) {
	tests := []struct {
		name  string
		extra exportFixtureEntry
		want  string
	}{
		{
			name: "state terminal control",
			extra: exportFixtureEntry{
				name: ".manager/state.json",
				data: []byte(`{"schema_version":1,"last_operation":"unsafe\u001b[2J"}`),
				mode: 0o600,
			},
			want: "manager state contains invalid or control characters",
		},
		{
			name:  "operation log terminal control",
			extra: exportFixtureEntry{name: ".manager/operations.log", data: []byte("safe\nunsafe\x1b[2J\n"), mode: 0o600},
			want:  "operation log contains invalid or terminal control characters",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "target")
			archivePath := filepath.Join(parent, "export.zip")
			cfg := fixtureImportConfig(t, target)
			writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), []exportFixtureEntry{test.extra}, nil)
			manager := newImportTestManager(t, target, &adminTestRunner{})
			if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q rejection, got %v", test.want, err)
			}
		})
	}
}

func TestPrepareImportedInstanceRevalidatesReplacedArchive(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	paths, _ := stack.NewPaths(target)
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, nil)
	inspected, err := inspectInstanceExport(archivePath, paths, ConfigPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), []exportFixtureEntry{
		{"workspace/../../escape", []byte("unsafe"), 0o600},
	}, nil)
	if _, err := prepareImportedInstance(inspected, paths); err == nil || !strings.Contains(err.Error(), "unsafe ZIP member") {
		t.Fatalf("expected replaced-archive revalidation, got %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced archive affected target: %v", err)
	}
}

func TestPrepareImportedInstanceRejectsReplacementWithSameMetadata(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	paths, _ := stack.NewPaths(target)
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, embeddedBackupWithPayload(t, `{"generation":1}`), nil, nil)
	inspected, err := inspectInstanceExport(archivePath, paths, ConfigPatch{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	writeExportFixture(t, archivePath, cfg, false, embeddedBackupWithPayload(t, `{"generation":2}`), nil, nil)
	if _, err := prepareImportedInstance(inspected, paths); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("expected content-digest replacement rejection, got %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced archive affected target: %v", err)
	}
}

func TestImportInstanceDockerFailureKeepsPublishedRecoveryFiles(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, nil)
	runner := &adminTestRunner{upFailures: 1}
	manager := newImportTestManager(t, target, runner)
	result, err := manager.ImportInstance(context.Background(), archivePath, ImportInstanceOptions{Start: true})
	if err == nil || !result.Applied || result.Started || result.Restored {
		t.Fatalf("unexpected Docker failure result=%#v err=%v", result, err)
	}
	if _, statErr := os.Stat(manager.Paths.Config); statErr != nil {
		t.Fatalf("published metadata was not retained for retry: %v", statErr)
	}
	if err := ValidateZIP(result.Plan.EmbeddedBackup); err != nil {
		t.Fatalf("embedded backup was not retained for retry: %v", err)
	}
}

func TestInstanceExportWorkspaceSymlinkContractRoundTrip(t *testing.T) {
	parent := t.TempDir()
	sourceRoot := filepath.Join(parent, "source")
	sourcePaths, err := stack.NewPaths(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{sourcePaths.Manager, sourcePaths.Data, sourcePaths.Workspace, sourcePaths.Backups} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := fixtureImportConfig(t, sourceRoot)
	if err := (config.Store{Paths: sourcePaths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	values := secrets.Values{
		secrets.PostgresPassword: "fixture-password",
		secrets.BetterAuthSecret: "fixture-auth-secret-at-least-32-chars",
		secrets.EncryptionKey:    "fixture-encryption-key-at-least-32-ch",
		secrets.SupervisorToken:  "fixture-supervisor-token-at-least-32",
	}
	if err := (secrets.Store{Paths: sourcePaths}).Save(values); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePaths.Compose, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourcePaths.Workspace, "notes.txt"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("notes.txt", filepath.Join(sourcePaths.Workspace, "notes-link")); err != nil {
		t.Fatal(err)
	}
	embeddedBackup := filepath.Join(sourcePaths.Backups, "rakazo-backup.zip")
	if err := os.WriteFile(embeddedBackup, validEmbeddedBackup(t), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := InstanceExportManifest{
		SchemaVersion: instanceExportSchema, CreatedAt: time.Now().UTC(), InstanceName: cfg.Name,
		TrackedImage: cfg.Image, RunningImage: "ghcr.io/elie222/rakazo/app@sha256:abc",
		RakazoBackup: filepath.Base(embeddedBackup), IncludesWorkspace: true, ContainsSecrets: true,
	}
	exportPath := filepath.Join(sourcePaths.Backups, "round-trip.zip")
	if err := writeInstanceExport(sourcePaths, manifest, embeddedBackup, exportPath, true); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	manager := newImportTestManager(t, target, &adminTestRunner{})
	name := "rakazo-round-trip"
	dashboardPort, apiPort, postgresPort := freeAdminPort(t), freeAdminPort(t), freeAdminPort(t)
	plan, err := manager.PlanImportInstance(exportPath, ImportInstanceOptions{Patch: ConfigPatch{
		Name: &name, WebPort: &dashboardPort, APIPort: &apiPort, PostgresPort: &postgresPort,
	}})
	if err != nil {
		t.Fatalf("successful export was not importable: %v", err)
	}
	if !plan.IncludesWorkspace {
		t.Fatal("round-trip plan lost workspace declaration")
	}

	unsafeLink := filepath.Join(sourcePaths.Workspace, "unsafe-link")
	if err := os.Symlink("../../outside", unsafeLink); err != nil {
		t.Fatal(err)
	}
	unsafeExport := filepath.Join(sourcePaths.Backups, "unsafe.zip")
	if err := writeInstanceExport(sourcePaths, manifest, embeddedBackup, unsafeExport, true); err == nil || !strings.Contains(err.Error(), "unsafe workspace symlink") {
		t.Fatalf("expected unsafe export symlink rejection, got %v", err)
	}
	if _, err := os.Lstat(unsafeExport); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe export was published: %v", err)
	}

	values["LD_PRELOAD"] = "/tmp/injected.so"
	if err := (secrets.Store{Paths: sourcePaths}).Save(values); err != nil {
		t.Fatal(err)
	}
	unsupportedCredentialsExport := filepath.Join(sourcePaths.Backups, "unsupported-credentials.zip")
	if err := writeInstanceExport(sourcePaths, manifest, embeddedBackup, unsupportedCredentialsExport, false); err == nil || !strings.Contains(err.Error(), "cannot be represented by the instance export contract") {
		t.Fatalf("expected unsupported credential export rejection, got %v", err)
	}
	if _, err := os.Lstat(unsupportedCredentialsExport); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported credential export was published: %v", err)
	}
}

func TestParseImportedSecretsRejectsUnexpectedKeysAndTerminalControls(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name: "unexpected environment key",
			content: secrets.PostgresPassword + "=password\n" +
				secrets.BetterAuthSecret + "=secret\n" +
				secrets.EncryptionKey + "=key\n" +
				secrets.SupervisorToken + "=token\n" +
				"LD_PRELOAD=/tmp/injected.so\n",
		},
		{
			name: "terminal escape in value",
			content: secrets.PostgresPassword + "=password\x1b[2J\n" +
				secrets.BetterAuthSecret + "=secret\n" +
				secrets.EncryptionKey + "=key\n" +
				secrets.SupervisorToken + "=token\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseImportedSecrets([]byte(test.content)); err == nil || !strings.Contains(err.Error(), "invalid archived credential") {
				t.Fatalf("expected unsafe credential rejection, got %v", err)
			}
		})
	}
}

func TestImportInstanceRejectsControlCharactersInSecrets(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), []exportFixtureEntry{
		{".manager/secrets.env", []byte(secrets.PostgresPassword + "=password\x1b[2J\n" + secrets.BetterAuthSecret + "=secret\n" + secrets.EncryptionKey + "=key\n" + secrets.SupervisorToken + "=token\n"), 0o600},
	}, nil)
	manager := newImportTestManager(t, target, &adminTestRunner{})
	if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "invalid archived credential") {
		t.Fatalf("expected terminal-control secret rejection, got %v", err)
	}
}

func TestImportInstanceRejectsUnsafeEmbeddedBackupIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"control character", "backup\n.zip"},
		{"excessive length", strings.Repeat("a", maxZIPArchiveBaseNameBytes-3) + ".zip"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "target")
			archivePath := filepath.Join(parent, "export.zip")
			cfg := fixtureImportConfig(t, target)
			writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, func(manifest *InstanceExportManifest) {
				manifest.RakazoBackup = test.value
			})
			manager := newImportTestManager(t, target, &adminTestRunner{})
			if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "unsafe embedded backup name") {
				t.Fatalf("expected unsafe embedded-backup identifier rejection, got %v", err)
			}
		})
	}
}

func TestImportInstanceRejectsUnsafeManifestRunningImage(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	archivePath := filepath.Join(parent, "export.zip")
	cfg := fixtureImportConfig(t, target)
	writeExportFixture(t, archivePath, cfg, false, validEmbeddedBackup(t), nil, func(manifest *InstanceExportManifest) {
		manifest.RunningImage = "ghcr.io/elie222/rakazo/app:v1\x1b[2J"
	})
	manager := newImportTestManager(t, target, &adminTestRunner{})
	if _, err := manager.PlanImportInstance(archivePath, ImportInstanceOptions{}); err == nil || !strings.Contains(err.Error(), "unsafe running image reference") {
		t.Fatalf("expected unsafe running-image rejection, got %v", err)
	}
}
