package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/fsutil"
	"github.com/nicolaeser/RakazoManager/internal/ports"
)

type InstallPreview struct {
	Config  config.Config `json:"config"`
	Compose string        `json:"compose"`
	Created bool          `json:"created"`
	Actions []string      `json:"actions"`
}

type RestorePreview struct {
	Source       string   `json:"source"`
	StagedPath   string   `json:"staged_path"`
	NeedsStaging bool     `json:"needs_staging"`
	Actions      []string `json:"actions"`
}

func (manager *Manager) PreviewManagedCompose(cfg config.Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	composePath := manager.Paths.Compose
	if !fsutil.FileExists(composePath) {
		composePath = manager.Paths.LegacyCompose()
	}
	existing, err := os.ReadFile(composePath)
	if err != nil {
		if os.IsNotExist(err) {
			return string(compose.Render(cfg)), nil
		}
		return "", fmt.Errorf("read Compose preview source: %w", err)
	}
	return string(compose.SyncManaged(existing, cfg)), nil
}

func (manager *Manager) PreviewRestore(requested string) (RestorePreview, error) {
	if err := manager.RequireInstalled(); err != nil {
		return RestorePreview{}, err
	}
	if strings.TrimSpace(requested) == "" {
		return RestorePreview{}, fmt.Errorf("backup path is required")
	}
	resolved := requested
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(manager.Paths.Backups, resolved)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return RestorePreview{}, fmt.Errorf("resolve backup: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return RestorePreview{}, fmt.Errorf("inspect backup: %w", err)
	}
	if !info.Mode().IsRegular() {
		return RestorePreview{}, fmt.Errorf("backup is not a regular file")
	}
	if !strings.HasSuffix(strings.ToLower(absolute), ".zip") {
		return RestorePreview{}, fmt.Errorf("full restore requires a Rakazo .zip backup")
	}
	if err := validateZIPArchiveBaseName(filepath.Base(absolute)); err != nil {
		return RestorePreview{}, fmt.Errorf("unsafe backup filename %q: %w", filepath.Base(absolute), err)
	}
	if err := validateZip(absolute); err != nil {
		return RestorePreview{}, err
	}
	staged := filepath.Join(manager.Paths.Backups, filepath.Base(absolute))
	needsStaging := filepath.Clean(filepath.Dir(absolute)) != filepath.Clean(manager.Paths.Backups)
	if needsStaging {
		if _, err := os.Lstat(staged); err == nil {
			return RestorePreview{}, fmt.Errorf("backup %s already exists in %s", filepath.Base(absolute), manager.Paths.Backups)
		} else if !os.IsNotExist(err) {
			return RestorePreview{}, err
		}
	}
	actions := []string{}
	if needsStaging {
		actions = append(actions, "copy the validated archive into backups/ with owner-only permissions")
	}
	actions = append(actions,
		"ensure the Rakazo stack is running",
		"acquire the instance operation lock",
		"create a pre-restore safety backup",
		"import the selected Rakazo backup with --force",
		"restart the Rakazo stack",
	)
	return RestorePreview{
		Source:       absolute,
		StagedPath:   staged,
		NeedsStaging: needsStaging,
		Actions:      actions,
	}, nil
}

func (manager *Manager) PreviewInstall(options InstallOptions) (InstallPreview, error) {
	created := !manager.ConfigStore.Exists()
	var cfg config.Config
	if created {
		if fsutil.FileExists(manager.Paths.Compose) || fsutil.FileExists(manager.Paths.LegacyCompose()) {
			return InstallPreview{}, fmt.Errorf("%s already exists but is not owned by Rakazo Manager", manager.Paths.Compose)
		}
		bindAddress := config.DefaultBindAddress
		if options.BindAll {
			bindAddress = config.PublicBindAddress
		}
		dashboardPort, apiPort, postgresPort, err := ports.Select(manager.Paths.Root, bindAddress, options.WebPort, options.APIPort, options.PostgresPort)
		if err != nil {
			return InstallPreview{}, err
		}
		cfg = config.New(manager.Paths.Root, options.Name, options.Image, dashboardPort, apiPort, postgresPort)
		cfg.BindAddress = bindAddress
		origin, originErr := config.ResolveOrigin(options.Origin, dashboardPort)
		if originErr != nil {
			return InstallPreview{}, originErr
		}
		cfg.Origin = origin
		if err := validateUniqueInstanceName(manager.Paths.Root, cfg.Name); err != nil {
			return InstallPreview{}, err
		}
	} else {
		loaded, err := manager.ConfigStore.Load()
		if err != nil {
			return InstallPreview{}, err
		}
		cfg = loaded
	}
	if options.HasRebuildComposeOnStart {
		cfg.RebuildComposeOnStart = options.RebuildComposeOnStart
	}
	if err := cfg.Validate(); err != nil {
		return InstallPreview{}, err
	}
	actions := []string{
		"write owner-only instance metadata and generated secrets",
		"create ./pg, ./data, ./workspace, and ./backups without removing existing contents",
		"write the managed docker-compose.yml shown below",
		"validate the generated Compose configuration",
	}
	if options.Pull {
		actions = append(actions, "pull the GHCR Rakazo app image")
	}
	if options.Start {
		actions = append(actions, "start Postgres, API, worker, web, and supervisor, then verify bind mounts")
	}
	return InstallPreview{
		Config:  cfg,
		Compose: string(compose.Render(cfg)),
		Created: created,
		Actions: actions,
	}, nil
}

type UpdatePreview struct {
	Root             string   `json:"root"`
	TrackedImage     string   `json:"tracked_image"`
	EffectiveImage   string   `json:"effective_image"`
	RunningImage     string   `json:"running_image,omitempty"`
	ContainerRunning bool     `json:"container_running"`
	Compose          string   `json:"compose"`
	Actions          []string `json:"actions"`
}

func (manager *Manager) PreviewUpdate(ctx context.Context) (UpdatePreview, error) {
	if err := manager.RequireInstalled(); err != nil {
		return UpdatePreview{}, err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return UpdatePreview{}, err
	}
	preview := UpdatePreview{
		Root:           manager.Paths.Root,
		TrackedImage:   cfg.Image,
		EffectiveImage: cfg.EffectiveImage(),
		Actions: []string{
			"ensure the Rakazo stack is running",
			"verify current host data and persistent bind mounts",
			"create a pre-update safety backup",
			"record the current running image for rollback",
			"clear any rollback pin and synchronize managed Compose fields",
			"pull the tracked image and force-recreate the Rakazo app containers",
			"verify bind mounts, host data, Rakazo version, and API health",
			"automatically restore the previous image if update verification fails",
		},
	}

	composePath := manager.Paths.Compose
	if !fsutil.FileExists(composePath) {
		composePath = manager.Paths.LegacyCompose()
	}
	existing, readErr := os.ReadFile(composePath)
	cfg.PinnedImage = ""
	switch {
	case readErr == nil:
		preview.Compose = string(compose.SyncManaged(existing, cfg))
	case errors.Is(readErr, os.ErrNotExist):
		preview.Compose = string(compose.Render(cfg))
	default:
		return UpdatePreview{}, fmt.Errorf("read Compose preview source: %w", readErr)
	}

	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return UpdatePreview{}, err
	}
	running, err := manager.Docker.ServiceRunningStatus(ctx)
	if err != nil {
		return UpdatePreview{}, err
	}
	preview.ContainerRunning = running
	if running {
		if image, imageErr := manager.currentImageReference(ctx); imageErr == nil {
			preview.RunningImage = strings.TrimSpace(image)
		} else {
			return UpdatePreview{}, imageErr
		}
	}
	return preview, nil
}
