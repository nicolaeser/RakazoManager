package manager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

func TestStatusAndDashboardDoNotCreateMissingCredentials(t *testing.T) {
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &adminTestRunner{root: root}
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	cfg := config.New(root, "rakazo-readonly", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := manager.ConfigStore.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, compose.Render(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Status(context.Background()); err != nil {

		t.Fatalf("Status: %v", err)
	}
	if _, err := os.Lstat(paths.Secrets); !os.IsNotExist(err) {
		t.Fatalf("status created secrets: %v", err)
	}
	if _, err := manager.Access(); err == nil {
		t.Fatal("Access should fail when secrets are missing")
	}
	if _, err := os.Lstat(paths.Secrets); !os.IsNotExist(err) {
		t.Fatalf("dashboard created secrets: %v", err)
	}

	if _, _, err := manager.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Lstat(paths.Secrets); err != nil {
		t.Fatalf("Load should create secrets: %v", err)
	}

	if filepath.Dir(paths.Secrets) != paths.Manager {
		t.Fatalf("unexpected secrets path %s", paths.Secrets)
	}
}
