package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

type rejectingCommandRunner struct{ t *testing.T }

func (runner rejectingCommandRunner) Run(_ context.Context, request command.Request) (command.Result, error) {
	runner.t.Helper()
	return command.Result{}, fmt.Errorf("unexpected external command %s %v", request.Name, request.Args)
}

func testApplication(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	output := &bytes.Buffer{}
	errOutput := &bytes.Buffer{}
	application := New(BuildInfo{Version: "v1.1.0", Commit: "abcdef0", Date: "2026-08-11T12:00:00Z"})
	application.In = strings.NewReader("")
	application.Out = output
	application.Err = errOutput
	application.Runner = rejectingCommandRunner{t: t}
	return application, output, errOutput
}

func TestGlobalHelpFlagsPrintCompleteHelpToStandardOutput(t *testing.T) {
	t.Parallel()
	for _, helpFlag := range []string{"--help", "-h"} {
		helpFlag := helpFlag
		t.Run(helpFlag, func(t *testing.T) {
			t.Parallel()
			application, output, errOutput := testApplication(t)
			if err := application.Run(context.Background(), []string{helpFlag}); err != nil {
				t.Fatal(err)
			}
			for _, expected := range []string{"Usage:\n  rakazo-manager", "instances list", "import-instance", "schedule set|show|remove|run", "completion bash|zsh|fish"} {
				if !strings.Contains(output.String(), expected) {
					t.Errorf("help output lacks %q:\n%s", expected, output.String())
				}
			}
			if errOutput.Len() != 0 {
				t.Fatalf("help wrote stderr: %q", errOutput.String())
			}
		})
	}
}

func TestVersionJSONDispatchProducesOneCleanEnvelope(t *testing.T) {
	t.Parallel()
	application, output, errOutput := testApplication(t)
	if err := application.Run(context.Background(), []string{"version", "--json"}); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		SchemaVersion int             `json:"schema_version"`
		Command       string          `json:"command"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid JSON %q: %v", output.String(), err)
	}
	if envelope.SchemaVersion != 1 || envelope.Command != "version" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
	if strings.Contains(output.String(), "\x1b[") || errOutput.Len() != 0 {
		t.Fatalf("machine output was contaminated: stdout=%q stderr=%q", output.String(), errOutput.String())
	}
}

func TestInstallDryRunPrintsExactPlanWithoutCreatingTarget(t *testing.T) {
	t.Parallel()
	application, output, errOutput := testApplication(t)
	target := filepath.Join(t.TempDir(), "planned-instance")
	err := application.Run(context.Background(), []string{
		"install", "--dry-run", "--no-pull", "--no-start",
		"--name", "rakazo-planned", "--web-port", "55119", "--api-port", "58642", target,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Install dry run", "rakazo-planned", "127.0.0.1:55119:5173", "./data:/data", "./pg:/var/lib/postgresql/data", `BETTER_AUTH_URL: "http://127.0.0.1:55119"`} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("dry-run output lacks %q:\n%s", expected, output.String())
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("install dry run created target: %v", err)
	}

	output.Reset()
	errOutput.Reset()
	if err := application.Run(context.Background(), []string{
		"install", "--dry-run", "--bind-all", "--no-pull", "--no-start",
		"--name", "rakazo-public-plan", "--web-port", "55121", "--api-port", "58644", target,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOutput.String(), "every host interface") {
		t.Fatalf("public-bind dry run omitted its risk warning: stderr=%q", errOutput.String())
	}
	if !strings.Contains(output.String(), `"0.0.0.0:55121:5173"`) {
		t.Fatalf("bind-all should publish web on 0.0.0.0:\n%s", output.String())
	}
	if !strings.Contains(output.String(), `"127.0.0.1:58644:3100"`) {
		t.Fatalf("bind-all should keep API on 127.0.0.1:\n%s", output.String())
	}
	if strings.Contains(output.String(), `"0.0.0.0:58644:3100"`) {
		t.Fatal("bind-all must not publish the API on 0.0.0.0")
	}

	output.Reset()
	errOutput.Reset()
	if err := application.Run(context.Background(), []string{
		"install", "--dry-run", "--no-pull", "--no-start",
		"--name", "rakazo-origin-plan", "--origin", "rakazo.example.com",
		"--web-port", "55123", "--api-port", "58646", target,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `BETTER_AUTH_URL: "https://rakazo.example.com"`) {
		t.Fatalf("origin dry-run should set Better Auth URL:\n%s", output.String())
	}
}

func TestInstancesAndConfigJSONCommandsAreReadOnly(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "main")
	paths := seedManagedInstance(t, root)
	invalid := filepath.Join(parent, "broken", ".manager")
	if err := os.MkdirAll(invalid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "instance.json"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	application, output, errOutput := testApplication(t)
	if err := application.Run(context.Background(), []string{"instances", "list", "--json", parent}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"command":"instances-list"`) || !strings.Contains(output.String(), paths.Root) || !strings.Contains(output.String(), "broken") {
		t.Fatalf("unexpected discovery JSON: %s", output.String())
	}
	if errOutput.Len() != 0 {
		t.Fatalf("JSON discovery wrote stderr: %s", errOutput.String())
	}

	output.Reset()
	if err := application.Run(context.Background(), []string{"config", "show", "--json", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"command":"config-show"`) || !strings.Contains(output.String(), `"name":"rakazo-test"`) {
		t.Fatalf("unexpected config JSON: %s", output.String())
	}
	if _, err := os.Stat(paths.Secrets); !os.IsNotExist(err) {
		t.Fatalf("read-only commands created credentials: %v", err)
	}
}

func TestConfigRestoreBackupsAndDecommissionDryRunsDoNotMutate(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "instance")
	paths := seedManagedInstance(t, root)
	for _, directory := range []string{paths.Data, paths.Workspace, paths.Backups} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	backup := filepath.Join(paths.Backups, "rakazo-manual-test.zip")
	writeCommandTestZIP(t, backup)
	if err := os.WriteFile(filepath.Join(paths.Backups, "another.tar.gz"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(paths.Config)
	if err != nil {
		t.Fatal(err)
	}

	application, output, errOutput := testApplication(t)
	if err := application.Run(context.Background(), []string{"config", "set", "--rebuild-on-start", "--dry-run", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Configuration dry run") {
		t.Fatalf("unexpected config plan: %s", output.String())
	}
	configAfter, _ := os.ReadFile(paths.Config)
	if !bytes.Equal(configBefore, configAfter) {
		t.Fatal("config dry run changed instance metadata")
	}

	output.Reset()
	if err := application.Run(context.Background(), []string{"restore", "--dry-run", filepath.Base(backup), root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Restore dry run") {
		t.Fatalf("unexpected restore plan: %s", output.String())
	}

	output.Reset()
	if err := application.Run(context.Background(), []string{"backups", "--json", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"command":"backups"`) || !strings.Contains(output.String(), filepath.Base(backup)) {
		t.Fatalf("unexpected backups JSON: %s", output.String())
	}

	output.Reset()
	if err := application.Run(context.Background(), []string{"decommission", "--dry-run", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Decommission dry run") {
		t.Fatalf("unexpected decommission plan: %s", output.String())
	}
	for _, retained := range []string{paths.Config, paths.Compose, paths.Data, paths.Workspace, paths.Backups} {
		if _, err := os.Lstat(retained); err != nil {
			t.Fatalf("dry run changed %s: %v", retained, err)
		}
	}

	output.Reset()
	errOutput.Reset()
	if err := application.Run(context.Background(), []string{"decommission", "--dry-run", "--delete-data", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOutput.String(), "cannot be recovered") {
		t.Fatalf("destructive decommission dry run omitted its risk warning: stderr=%q", errOutput.String())
	}
	if _, err := os.Lstat(paths.Data); err != nil {
		t.Fatalf("destructive decommission dry run changed data: %v", err)
	}
}

func seedManagedInstance(t *testing.T, root string) stack.Paths {
	t.Helper()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(root, "rakazo-test", config.DefaultImage, 55120, 58643, 55433)
	if err := (config.Store{Paths: paths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, []byte("services:\n  api:\n    image: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return paths
}

func writeCommandTestZIP(t *testing.T, filename string) {
	t.Helper()
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	member, err := archive.Create("backup.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
