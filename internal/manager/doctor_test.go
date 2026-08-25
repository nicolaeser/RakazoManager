package manager

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

func TestEvaluateComposeMountPolicyAcceptsDuplicatedManagedBinds(t *testing.T) {
	cfg := config.New(t.TempDir(), "rakazo-test", config.DefaultImage, 9120, 8650, 5433)
	base := string(compose.Render(cfg))
	var report DoctorReport
	evaluateComposeMountPolicy(&report, "Compose mount policy", base, true)
	if len(report.Checks) != 1 || report.Checks[0].Level != CheckPass {
		t.Fatalf("expected pass for generated compose, got %#v", report.Checks)
	}
	if strings.Count(base, "./data:/data") < 2 {
		t.Fatal("fixture no longer duplicates managed binds")
	}
}

func TestEvaluateComposeMountPolicyOverrideForbiddenAndExtra(t *testing.T) {
	var report DoctorReport
	evaluateComposeMountPolicy(&report, "Compose override mounts", "services:\n  api:\n    environment:\n      DATABASE_URL: ${POSTGRES_PASSWORD}\n", false)
	if len(report.Checks) != 1 || report.Checks[0].Level != CheckFail || !strings.Contains(report.Checks[0].Detail, "${") {
		t.Fatalf("expected interpolation failure, got %#v", report.Checks)
	}

	report = DoctorReport{}
	evaluateComposeMountPolicy(&report, "Compose override mounts", "services:\n  api:\n    volumes:\n      - /home/user/.openclaw:/openclaw:ro\n", false)
	if len(report.Checks) != 1 || report.Checks[0].Level != CheckWarn {
		t.Fatalf("expected extra-mount warning, got %#v", report.Checks)
	}
}

func TestClearStaleLockThroughManager(t *testing.T) {
	root := t.TempDir()
	paths, err := stack.NewPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	runner := &adminTestRunner{root: root}
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	cfg := config.New(root, "rakazo-doctor-lock", config.DefaultImage, freeAdminPort(t), freeAdminPort(t), freeAdminPort(t))
	if err := manager.ConfigStore.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, compose.Render(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Lock, []byte("pid=2147483646 operation=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleared, detail, err := manager.ClearStaleLock()
	if err != nil || !cleared || detail == "" {
		t.Fatalf("cleared=%v detail=%q err=%v", cleared, detail, err)
	}
	if _, err := os.Lstat(paths.Lock); !os.IsNotExist(err) {
		t.Fatalf("lock remains: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(root, "docker-compose.yml")); err != nil {
		t.Fatal(err)
	}
}
