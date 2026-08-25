package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/ports"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
)

type APIHealth struct {
	OK           bool   `json:"ok"`
	Runtime      string `json:"runtime"`
	Sandbox      string `json:"sandbox"`
	Jobs         string `json:"jobs"`
	Realtime     string `json:"realtime"`
	Revision     string `json:"revision"`
	Version      string `json:"version,omitempty"`
	AuthRequired bool   `json:"auth_required,omitempty"`
}

type DashboardHealth = APIHealth

type CheckLevel string

const (
	CheckPass CheckLevel = "PASS"
	CheckWarn CheckLevel = "WARN"
	CheckFail CheckLevel = "FAIL"
)

type DoctorCheck struct {
	Level  CheckLevel
	Name   string
	Detail string
}

type DoctorReport struct {
	Checks []DoctorCheck
}

const (
	dashboardReadyTimeout = 5 * time.Minute
	apiReadyTimeout       = dashboardReadyTimeout
)

func (report *DoctorReport) add(level CheckLevel, name, detail string) {
	report.Checks = append(report.Checks, DoctorCheck{Level: level, Name: name, Detail: detail})
}

func (report DoctorReport) Healthy() bool {
	for _, check := range report.Checks {
		if check.Level == CheckFail {
			return false
		}
	}
	return true
}

func (manager *Manager) APIHealth(ctx context.Context) (APIHealth, error) {
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return APIHealth{}, err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", cfg.APIPort)
	return requestAPIHealth(ctx, url)
}

func (manager *Manager) DashboardHealth(ctx context.Context) (DashboardHealth, error) {
	return manager.APIHealth(ctx)
}

func requestAPIHealth(ctx context.Context, url string) (APIHealth, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return APIHealth{}, err
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return APIHealth{}, fmt.Errorf("API health request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return APIHealth{}, fmt.Errorf("API health returned HTTP %d", response.StatusCode)
	}
	var health APIHealth
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&health); err != nil {
		return APIHealth{}, fmt.Errorf("decode API health: %w", err)
	}
	if !health.OK {
		return health, fmt.Errorf("API reported ok=false")
	}
	return health, nil
}

func (manager *Manager) waitForAPI(ctx context.Context, timeout time.Duration) (APIHealth, error) {
	if manager.DashboardWait != nil {
		return manager.DashboardWait(ctx, timeout)
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		health, err := manager.APIHealth(ctx)
		if err == nil {
			return health, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return APIHealth{}, fmt.Errorf("API did not become healthy within %s: %w", timeout, lastErr)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return APIHealth{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (manager *Manager) waitForDashboard(ctx context.Context, timeout time.Duration) (DashboardHealth, error) {
	return manager.waitForAPI(ctx, timeout)
}

func (manager *Manager) Doctor(ctx context.Context) DoctorReport {
	var report DoctorReport
	if err := manager.RequireInstalled(); err != nil {
		report.add(CheckFail, "Installation", err.Error())
		return report
	}

	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		report.add(CheckFail, "Instance metadata", err.Error())
		return report
	}
	report.add(CheckPass, "Instance metadata", fmt.Sprintf("%s, bind %s, origin %s, web %d, API %d, Postgres %d", cfg.Name, cfg.BindAddress, cfg.PublicOrigin(), cfg.WebPort, cfg.APIPort, cfg.PostgresPort))
	if cfg.BindAddress == config.PublicBindAddress {
		report.add(CheckWarn, "Network exposure", "web is published on every host interface (0.0.0.0); API and Postgres stay on 127.0.0.1")
	} else {
		report.add(CheckPass, "Network exposure", "web, API, and Postgres are localhost-only")
	}
	if conflicts := ports.Conflicts(manager.Paths.Root, cfg.BindAddress, cfg.WebPort, cfg.APIPort, cfg.PostgresPort); len(conflicts) > 0 {
		for _, detail := range conflicts {
			report.add(CheckFail, "Port conflict", detail)
		}
	} else {
		report.add(CheckPass, "Port uniqueness", fmt.Sprintf("web %d, API %d, and Postgres %d are not reserved by sibling Rakazo instances", cfg.WebPort, cfg.APIPort, cfg.PostgresPort))
	}

	values, err := manager.SecretStore.Load()
	if err != nil {
		report.add(CheckFail, "Instance secrets", err.Error())
	} else if values[secrets.BetterAuthSecret] == "" ||
		values[secrets.EncryptionKey] == "" ||
		values[secrets.SupervisorToken] == "" ||
		values[secrets.PostgresPassword] == "" {
		report.add(CheckFail, "Instance secrets", "one or more required values are empty")
	} else {
		report.add(CheckPass, "Instance secrets", "required values are present")
	}

	for _, directory := range []struct {
		name string
		path string
		hint string
	}{
		{"Manager directory", manager.Paths.Manager, ""},
		{"Postgres data", manager.Paths.Postgres, "bind-mounted at ./pg"},
		{"Application data", manager.Paths.App, "bind-mounted at ./data"},
		{"Project workspace", manager.Paths.Workspace, "optional host project files"},
		{"Backups", manager.Paths.Backups, "host archive directory"},
	} {
		name, path := directory.name, directory.path
		info, statErr := os.Stat(path)
		if statErr != nil {
			report.add(CheckFail, name, statErr.Error())
			continue
		}
		if !info.IsDir() {
			report.add(CheckFail, name, path+" is not a directory")
			continue
		}
		if writeErr := probeWritable(path); writeErr != nil {
			report.add(CheckFail, name, "not writable: "+writeErr.Error())
		} else if directory.hint != "" {
			report.add(CheckPass, name, path+" — "+directory.hint)
		} else {
			report.add(CheckPass, name, path)
		}
	}

	if info, err := os.Stat("/var/run/docker.sock"); err != nil {
		report.add(CheckFail, "Docker socket", "supervisor needs /var/run/docker.sock to create bot computers: "+err.Error())
	} else if info.Mode()&os.ModeSocket == 0 && !info.Mode().IsRegular() {
		report.add(CheckWarn, "Docker socket", "/var/run/docker.sock exists but is not a socket; Docker Desktop may still work")
	} else {
		report.add(CheckPass, "Docker socket", "/var/run/docker.sock is present for the supervisor")
	}

	checkPrivateMode(&report, "Manager permissions", manager.Paths.Manager, 0o700)
	checkPrivateMode(&report, "Metadata permissions", manager.Paths.Config, 0o600)
	checkPrivateMode(&report, "Credential permissions", manager.Paths.Secrets, 0o600)

	composePath := manager.Paths.Compose
	if _, err := os.Stat(composePath); err != nil {
		composePath = manager.Paths.LegacyCompose()
	}
	composeContent, err := os.ReadFile(composePath)
	if err != nil {
		report.add(CheckFail, "Compose mount policy", err.Error())
	} else {
		evaluateComposeMountPolicy(&report, "Compose mount policy", string(composeContent), true)
	}
	overridePath := manager.Paths.ComposeOverride()
	if overrideContent, overrideErr := os.ReadFile(overridePath); overrideErr == nil {
		evaluateComposeMountPolicy(&report, "Compose override mounts", string(overrideContent), false)
	} else if !os.IsNotExist(overrideErr) {
		report.add(CheckWarn, "Compose override mounts", overrideErr.Error())
	}

	if manager.appStatePresent() {
		report.add(CheckPass, "Application state on host", "durable files found under data/ (update will preserve them)")
	} else {
		report.add(CheckWarn, "Application state on host", "no bot homes yet under data/; empty until the first computer starts")
	}

	if available, diskErr := availableBytes(manager.Paths.Root); diskErr != nil {
		report.add(CheckWarn, "Free disk space", diskErr.Error())
	} else {
		level := CheckPass
		if available < 5<<30 {
			level = CheckWarn
		}
		report.add(level, "Free disk space", humanBytes(available)+" available")
	}

	backups, err := manager.ListBackups()
	if err != nil {
		report.add(CheckWarn, "Backups", err.Error())
	} else if len(backups) == 0 {
		report.add(CheckWarn, "Backups", "no Rakazo backup exists yet")
	} else {
		report.add(CheckPass, "Backups", fmt.Sprintf("%d archive(s)", len(backups)))
	}

	if err := manager.Docker.CheckCLI(ctx); err != nil {
		report.add(CheckFail, "Docker CLI and Compose", err.Error())
		return report
	}
	report.add(CheckPass, "Docker CLI and Compose", "available")
	if err := manager.Docker.ValidateCompose(ctx); err != nil {
		report.add(CheckFail, "Compose validation", err.Error())
	} else {
		report.add(CheckPass, "Compose validation", "valid")
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		report.add(CheckFail, "Docker daemon", err.Error())
		return report
	}
	report.add(CheckPass, "Docker daemon", "available")
	if !manager.Docker.ServiceRunning(ctx) {
		report.add(CheckWarn, "Rakazo stack", "API is not running")
		for _, item := range []struct {
			port int
			role string
			bind string
		}{
			{cfg.WebPort, "web", cfg.BindAddress},
			{cfg.APIPort, "API", config.DefaultBindAddress},
			{cfg.PostgresPort, "Postgres", config.DefaultBindAddress},
		} {
			if !ports.Available(item.bind, item.port) {
				report.add(CheckWarn, "Port availability", fmt.Sprintf("%s port %d is already in use on %s; start may fail", item.role, item.port, item.bind))
			}
		}
		return report
	}
	report.add(CheckPass, "Rakazo stack", "API is running")

	if err := manager.verifyContainerBinds(ctx); err != nil {
		report.add(CheckFail, "Live bind mounts", err.Error())
	} else {
		report.add(CheckPass, "Live bind mounts", "API /data and Postgres data match host instance paths")
	}

	health, err := manager.APIHealth(ctx)
	if err != nil {
		report.add(CheckFail, "API health", err.Error())
	} else {
		report.add(CheckPass, "API health", fmt.Sprintf("healthy runtime=%s sandbox=%s jobs=%s", health.Runtime, health.Sandbox, health.Jobs))
	}
	return report
}

func evaluateComposeMountPolicy(report *DoctorReport, name, composeText string, requireManagedBinds bool) {
	required := []string{"./pg:/var/lib/postgresql/data", "./data:/data", "/var/run/docker.sock:/var/run/docker.sock"}
	forbidden := []string{"${"}
	if requireManagedBinds {
		missing := make([]string, 0, len(required))
		for _, value := range required {

			if strings.Count(composeText, value) < 1 {
				missing = append(missing, value)
			}
		}
		for _, value := range forbidden {
			if strings.Contains(composeText, value) {
				report.add(CheckFail, name, "base Compose contains forbidden mount material ("+value+"); run install to repair")
				return
			}
		}
		if len(missing) > 0 {
			report.add(CheckFail, name, "base Compose is missing required bind(s): "+strings.Join(missing, ", ")+"; run install to repair")
			return
		}
		report.add(CheckPass, name, "required binds present: Postgres data, app data, and Docker socket")
		return
	}

	for _, value := range forbidden {
		if strings.Contains(composeText, value) {
			report.add(CheckFail, name, "override contains forbidden mount material ("+value+")")
			return
		}
	}

	lines := strings.Split(composeText, "\n")
	extra := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		volume := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		volume = strings.Trim(volume, `"'`)
		if !strings.Contains(volume, ":") {
			continue
		}

		if volume == "./pg:/var/lib/postgresql/data" || volume == "./data:/data" || volume == "./backups:/backups" || volume == "/var/run/docker.sock:/var/run/docker.sock" {
			continue
		}
		if strings.HasPrefix(volume, "./") || strings.HasPrefix(volume, "/") || strings.Contains(volume, ":/") {
			extra++
		}
	}
	if extra > 0 {
		report.add(CheckWarn, name, fmt.Sprintf("override declares %d additional volume mount(s); review them for host exposure", extra))
		return
	}
	report.add(CheckPass, name, "no forbidden mounts in docker-compose.override.yml")
}

func (manager *Manager) ClearStaleLock() (cleared bool, detail string, err error) {
	if err := manager.RequireInstalled(); err != nil {
		return false, "", err
	}
	return manager.StateStore.ClearStaleLock()
}

func probeWritable(directory string) error {
	file, err := os.CreateTemp(directory, ".rakazo-manager-doctor-*")
	if err != nil {
		return err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return os.Remove(path)
}

func checkPrivateMode(report *DoctorReport, name, path string, expected os.FileMode) {
	info, err := os.Stat(path)
	if err != nil {
		report.add(CheckFail, name, err.Error())
		return
	}
	actual := info.Mode().Perm()
	if actual != expected {
		report.add(CheckWarn, name, fmt.Sprintf("%s has mode %04o; expected %04o", path, actual, expected))
		return
	}
	report.add(CheckPass, name, fmt.Sprintf("mode %04o", actual))
}

func availableBytes(path string) (uint64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return uint64(stats.Bavail) * uint64(stats.Bsize), nil
}

func humanBytes(value uint64) string {
	const (
		gib = uint64(1 << 30)
		mib = uint64(1 << 20)
	)
	if value >= gib {
		return fmt.Sprintf("%.1f GiB", float64(value)/float64(gib))
	}
	return fmt.Sprintf("%.1f MiB", float64(value)/float64(mib))
}
