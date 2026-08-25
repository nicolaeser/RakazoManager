package manager

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

func TestAccessRequiresExistingSecrets(t *testing.T) {
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	mgr := New(paths, &adminTestRunner{root: root}, strings.NewReader(""), io.Discard, io.Discard)
	cfg := config.New(root, "rakazo-access", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := mgr.ConfigStore.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, compose.Render(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Access(); err == nil {
		t.Fatal("Access should fail without secrets")
	}
	if _, _, err := mgr.Load(); err != nil {
		t.Fatal(err)
	}
	access, err := mgr.Access()
	if err != nil {
		t.Fatal(err)
	}
	if access.URL == "" || access.API == "" {
		t.Fatalf("incomplete access info: %+v", access)
	}
}
