package manager

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

type previewRunner struct{}

func (previewRunner) Run(context.Context, command.Request) (command.Result, error) {
	return command.Result{}, nil
}

func TestPreviewInstallDoesNotCreateInstanceFiles(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "new-instance")
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(paths, previewRunner{}, strings.NewReader(""), io.Discard, io.Discard)

	preview, err := manager.PreviewInstall(InstallOptions{
		Name:    "rakazo-preview",
		WebPort: 49119,
		APIPort: 48642,
		Pull:    true,
		Start:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Created {
		t.Fatal("preview should describe a new instance")
	}
	if preview.Config.Name != "rakazo-preview" {
		t.Fatalf("unexpected instance name %q", preview.Config.Name)
	}
	for _, want := range []string{
		`image: "ghcr.io/elie222/rakazo/app:edge"`,
		`"127.0.0.1:49119:5173"`,
		`"127.0.0.1:48642:3100"`,
		"./data:/data",
		"./pg:/var/lib/postgresql/data",
		"./backups:/backups",
	} {
		if !strings.Contains(preview.Compose, want) {
			t.Errorf("Compose preview does not contain %q", want)
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry-run preview created target root: %v", err)
	}
}

func TestPreviewInstallRejectsUnownedCompose(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(paths, previewRunner{}, strings.NewReader(""), io.Discard, io.Discard)
	_, err = manager.PreviewInstall(InstallOptions{WebPort: 49120, APIPort: 48643})
	if err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Fatalf("expected unowned Compose rejection, got %v", err)
	}
}

func TestPreviewRestoreExternalArchiveDoesNotStageIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.New(root, "rakazo-restore-preview", config.DefaultImage, 49121, 48644, 5433)
	if err := (config.Store{Paths: paths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, compose.Render(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Backups, 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "rakazo-external.zip")
	writeTestZip(t, external)
	manager := New(paths, previewRunner{}, strings.NewReader(""), io.Discard, io.Discard)

	preview, err := manager.PreviewRestore(external)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.NeedsStaging || preview.Source != external {
		t.Fatalf("unexpected restore preview: %#v", preview)
	}
	if _, err := os.Lstat(preview.StagedPath); !os.IsNotExist(err) {
		t.Fatalf("restore preview staged an archive: %v", err)
	}
}

func TestPreviewUpdateDoesNotChangePinnedConfigOrCompose(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.New(root, "rakazo-update-preview", config.DefaultImage, 49122, 48645, 5433)
	cfg.PinnedImage = "ghcr.io/elie222/rakazo/app@sha256:" + strings.Repeat("a", 64)
	if err := (config.Store{Paths: paths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	composeBefore := compose.Render(cfg)
	if err := os.WriteFile(paths.Compose, composeBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(paths, previewRunner{}, strings.NewReader(""), io.Discard, io.Discard)

	preview, err := manager.PreviewUpdate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if preview.ContainerRunning || preview.EffectiveImage != cfg.PinnedImage {
		t.Fatalf("unexpected update preview: %#v", preview)
	}
	if !strings.Contains(preview.Compose, `image: "`+cfg.Image+`"`) || strings.Contains(preview.Compose, cfg.PinnedImage) {
		t.Fatalf("preview does not show rollback-pin clearing:\n%s", preview.Compose)
	}
	configAfter, _ := os.ReadFile(paths.Config)
	composeAfter, _ := os.ReadFile(paths.Compose)
	if !bytes.Equal(configBefore, configAfter) || !bytes.Equal(composeBefore, composeAfter) {
		t.Fatal("update preview changed instance metadata or Compose")
	}
}
