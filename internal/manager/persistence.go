package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/docker"
)

type mountInfo struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

func (manager *Manager) assertHostPersistence() error {
	for _, directory := range []struct {
		label string
		path  string
	}{
		{"Postgres data", manager.Paths.Postgres},
		{"application data", manager.Paths.App},
		{"project workspace", manager.Paths.Workspace},
		{"backups", manager.Paths.Backups},
	} {
		info, err := os.Stat(directory.path)
		if err != nil {
			return fmt.Errorf("%s directory missing (%s): %w", directory.label, directory.path, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s path is not a directory: %s", directory.label, directory.path)
		}
	}
	return nil
}

func (manager *Manager) assertDataSurvived(preUpdateHadState bool) error {
	if err := manager.assertHostPersistence(); err != nil {
		return err
	}
	if !preUpdateHadState {
		return nil
	}
	if !manager.appStatePresent() {
		return fmt.Errorf(
			"application data under %s is missing after container recreate; host bind mount may be wrong — do not delete %s",
			manager.Paths.App,
			manager.Paths.App,
		)
	}
	return nil
}

func (manager *Manager) appStatePresent() bool {
	markers := []string{
		filepath.Join(manager.Paths.App, "homes"),
		filepath.Join(manager.Paths.App, "home-revisions"),
		filepath.Join(manager.Paths.App, "artifacts"),
	}
	for _, marker := range markers {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

func (manager *Manager) durableStatePresent() bool {
	return manager.appStatePresent()
}

func (manager *Manager) verifyContainerBinds(ctx context.Context) error {
	if err := manager.verifyServiceBind(ctx, docker.PrimaryService, map[string]string{
		"/data":    resolvePath(manager.Paths.App),
		"/backups": resolvePath(manager.Paths.Backups),
	}); err != nil {
		return err
	}
	return manager.verifyServiceBind(ctx, "postgres", map[string]string{
		"/var/lib/postgresql/data": resolvePath(manager.Paths.Postgres),
	})
}

func (manager *Manager) verifyServiceBind(ctx context.Context, service string, expected map[string]string) error {
	containerID, err := manager.Docker.ComposeOutput(ctx, "ps", "-q", service)
	if err != nil {
		return fmt.Errorf("resolve %s container for mount check: %w", service, err)
	}
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return fmt.Errorf("%s container is not running; cannot verify bind mounts", service)
	}
	raw, err := manager.Docker.DockerOutput(ctx, "inspect", "--format", "{{json .Mounts}}", containerID)
	if err != nil {
		return fmt.Errorf("inspect %s mounts: %w", service, err)
	}
	var mounts []mountInfo
	if err := json.Unmarshal([]byte(raw), &mounts); err != nil {
		return fmt.Errorf("parse %s mounts: %w", service, err)
	}
	byTarget := make(map[string]mountInfo, len(mounts))
	for _, mount := range mounts {
		byTarget[mount.Destination] = mount
	}
	for target, wantSource := range expected {
		mount, ok := byTarget[target]
		if !ok {
			return fmt.Errorf("%s is missing required bind mount %s → %s", service, wantSource, target)
		}
		if !strings.EqualFold(mount.Type, "bind") {
			return fmt.Errorf("%s mount %s is type %q, expected bind", service, target, mount.Type)
		}
		got := resolvePath(mount.Source)
		if got != wantSource {
			return fmt.Errorf("%s mount %s points at %s, expected %s", service, target, got, wantSource)
		}
	}
	return nil
}

func resolvePath(path string) string {
	cleaned := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

func (manager *Manager) preUpdateSnapshot() (hadState bool, err error) {
	if err := manager.assertHostPersistence(); err != nil {
		return false, err
	}
	return manager.appStatePresent(), nil
}
