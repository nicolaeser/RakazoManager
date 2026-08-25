package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/stack"
)

func testStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Manager, 0o700); err != nil {
		t.Fatal(err)
	}
	return Store{Paths: paths}
}

func TestLoadOrCreateGeneratesIndependentSecrets(t *testing.T) {
	store := testStore(t)
	values, created, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected secrets to be created")
	}
	if values[PostgresPassword] == "" || values[BetterAuthSecret] == "" || values[EncryptionKey] == "" || values[SupervisorToken] == "" {
		t.Fatalf("missing required secrets: %#v", values)
	}
	if values[BetterAuthSecret] == values[SupervisorToken] {
		t.Fatal("supervisor token must differ from the auth secret")
	}
	if values[DatabaseURL] == "" {
		t.Fatal("DATABASE_URL should be derived")
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[BetterAuthSecret] != values[BetterAuthSecret] {
		t.Fatal("secrets were rotated on reload")
	}
}

func TestSetOptionalOpenRouterKey(t *testing.T) {
	store := testStore(t)
	values, _, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetOptional(values, OpenRouterAPIKey, "sk-test"); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded[OpenRouterAPIKey] != "sk-test" {
		t.Fatalf("got %q", loaded[OpenRouterAPIKey])
	}
	info, err := os.Stat(filepath.Join(store.Paths.Manager, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets mode %04o", info.Mode().Perm())
	}
}
