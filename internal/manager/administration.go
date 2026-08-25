package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/fsutil"
	"github.com/nicolaeser/RakazoManager/internal/ports"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

type InstanceInfo struct {
	Root        string        `json:"root"`
	Config      config.Config `json:"config"`
	ComposePath string        `json:"compose_path"`
}

type InvalidInstance struct {
	Root   string `json:"root"`
	Reason string `json:"reason"`
}

type InstanceDiscovery struct {
	Parent    string            `json:"parent"`
	Instances []InstanceInfo    `json:"instances"`
	Invalid   []InvalidInstance `json:"invalid"`
}

func DiscoverInstances(parent string) (InstanceDiscovery, error) {
	absolute, err := filepath.Abs(parent)
	if err != nil {
		return InstanceDiscovery{}, fmt.Errorf("resolve instance collection: %w", err)
	}
	absolute = filepath.Clean(absolute)
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return InstanceDiscovery{}, fmt.Errorf("list instance collection %s: %w", absolute, err)
	}
	result := InstanceDiscovery{
		Parent:    absolute,
		Instances: []InstanceInfo{},
		Invalid:   []InvalidInstance{},
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(absolute, entry.Name())
		paths, pathsErr := stack.NewPaths(root)
		if pathsErr != nil {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: pathsErr.Error()})
			continue
		}
		managerInfo, statErr := os.Lstat(paths.Manager)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: fmt.Sprintf("inspect manager directory: %v", statErr)})
			continue
		}
		if managerInfo.Mode()&os.ModeSymlink != 0 || !managerInfo.IsDir() {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: ".manager is not a real directory"})
			continue
		}
		configInfo, statErr := os.Lstat(paths.Config)
		if statErr != nil {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: fmt.Sprintf("inspect instance metadata: %v", statErr)})
			continue
		}
		if configInfo.Mode()&os.ModeSymlink != 0 || !configInfo.Mode().IsRegular() {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: "instance metadata is not a real regular file"})
			continue
		}
		cfg, loadErr := (config.Store{Paths: paths}).Load()
		if loadErr != nil {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: loadErr.Error()})
			continue
		}
		composePath := ""
		composeInvalid := ""
		for _, candidate := range []string{paths.Compose, paths.LegacyCompose()} {
			composeInfo, composeErr := os.Lstat(candidate)
			if errors.Is(composeErr, os.ErrNotExist) {
				continue
			}
			if composeErr != nil {
				composeInvalid = fmt.Sprintf("inspect managed Compose file: %v", composeErr)
				break
			}
			if composeInfo.Mode()&os.ModeSymlink != 0 || !composeInfo.Mode().IsRegular() {
				composeInvalid = "managed Compose file is not a real regular file"
				break
			}
			composePath = candidate
			break
		}
		if composeInvalid != "" {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: composeInvalid})
			continue
		}
		if composePath == "" {
			result.Invalid = append(result.Invalid, InvalidInstance{Root: root, Reason: "managed Compose file is missing"})
			continue
		}
		result.Instances = append(result.Instances, InstanceInfo{Root: root, Config: cfg, ComposePath: composePath})
	}
	return result, nil
}

type ConfigPatch struct {
	Name                  *string `json:"name,omitempty"`
	Image                 *string `json:"image,omitempty"`
	WebPort               *int    `json:"web_port,omitempty"`
	APIPort               *int    `json:"api_port,omitempty"`
	PostgresPort          *int    `json:"postgres_port,omitempty"`
	BindAddress           *string `json:"bind_address,omitempty"`
	Origin                *string `json:"origin,omitempty"`
	RebuildComposeOnStart *bool   `json:"rebuild_compose_on_start,omitempty"`
}

type ConfigChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type ConfigPlan struct {
	Before                config.Config  `json:"before"`
	After                 config.Config  `json:"after"`
	Changes               []ConfigChange `json:"changes"`
	RequiresComposeUpdate bool           `json:"requires_compose_update"`
	RequiresRecreate      bool           `json:"requires_recreate"`
	RuntimeInspected      bool           `json:"runtime_inspected"`
	ServiceExists         bool           `json:"service_exists"`
	ServiceRunning        bool           `json:"service_running"`
	RuntimeAction         string         `json:"runtime_action,omitempty"`
}

func (manager *Manager) ShowConfig() (config.Config, error) {
	if err := manager.validateConfigLayout(false, false); err != nil {
		return config.Config{}, err
	}
	if err := manager.RequireInstalled(); err != nil {
		return config.Config{}, err
	}
	return manager.ConfigStore.Load()
}

func (manager *Manager) PlanConfigPatch(patch ConfigPatch) (ConfigPlan, error) {
	before, err := manager.ShowConfig()
	if err != nil {
		return ConfigPlan{}, err
	}
	plan, err := planConfigPatch(before, patch)
	if err != nil {
		return ConfigPlan{}, err
	}
	if plan.RequiresComposeUpdate {
		if err := manager.validateConfigLayout(true, true); err != nil {
			return ConfigPlan{}, err
		}
	}

	if err := manager.validateConfigPlan(plan, false); err != nil {
		return ConfigPlan{}, err
	}
	return plan, nil
}

func planConfigPatch(before config.Config, patch ConfigPatch) (ConfigPlan, error) {
	after := before
	if patch.Name != nil {
		after.Name = *patch.Name
	}
	if patch.Image != nil {
		after.Image = *patch.Image
		after.PinnedImage = ""
	}
	if patch.WebPort != nil {
		after.WebPort = *patch.WebPort
	}
	if patch.APIPort != nil {
		after.APIPort = *patch.APIPort
	}
	if patch.PostgresPort != nil {
		after.PostgresPort = *patch.PostgresPort
	}
	if patch.BindAddress != nil {
		after.BindAddress = *patch.BindAddress
	}
	if patch.Origin != nil {
		origin, err := config.ResolveOrigin(*patch.Origin, after.WebPort)
		if err != nil {
			return ConfigPlan{}, err
		}
		after.Origin = origin
	}
	if patch.RebuildComposeOnStart != nil {
		after.RebuildComposeOnStart = *patch.RebuildComposeOnStart
	}
	if err := after.Validate(); err != nil {
		return ConfigPlan{}, fmt.Errorf("validate requested configuration: %w", err)
	}

	plan := ConfigPlan{Before: before, After: after}
	appendChange := func(field string, oldValue, newValue any, compose bool) {
		if fmt.Sprint(oldValue) == fmt.Sprint(newValue) {
			return
		}
		plan.Changes = append(plan.Changes, ConfigChange{Field: field, Before: oldValue, After: newValue})
		if compose {
			plan.RequiresComposeUpdate = true
			plan.RequiresRecreate = true
		}
	}
	appendChange("name", before.Name, after.Name, true)
	appendChange("image", before.Image, after.Image, true)
	appendChange("pinned_image", before.PinnedImage, after.PinnedImage, true)
	appendChange("web_port", before.WebPort, after.WebPort, true)
	appendChange("api_port", before.APIPort, after.APIPort, true)
	appendChange("postgres_port", before.PostgresPort, after.PostgresPort, true)
	appendChange("bind_address", before.BindAddress, after.BindAddress, true)
	appendChange("origin", before.Origin, after.Origin, true)
	appendChange("rebuild_compose_on_start", before.RebuildComposeOnStart, after.RebuildComposeOnStart, false)
	return plan, nil
}

func (manager *Manager) ApplyConfigPatch(ctx context.Context, patch ConfigPatch, dryRun bool) (plan ConfigPlan, operationErr error) {
	before, err := manager.ShowConfig()
	if err != nil {
		return ConfigPlan{}, err
	}
	plan, err = planConfigPatch(before, patch)
	if err != nil || len(plan.Changes) == 0 {
		return plan, err
	}
	if dryRun {
		if plan.RequiresComposeUpdate {
			if err := manager.validateConfigLayout(true, true); err != nil {
				return plan, err
			}
			if err := manager.Docker.CheckDaemon(ctx); err != nil {
				return plan, err
			}
			running, err := manager.Docker.ServiceRunningStatus(ctx)
			if err != nil {
				return plan, fmt.Errorf("inspect running Rakazo API: %w", err)
			}
			exists := running
			if !exists {
				exists, err = manager.Docker.ServiceExists(ctx)
				if err != nil {
					return plan, fmt.Errorf("inspect existing Rakazo API: %w", err)
				}
			}
			setConfigRuntime(&plan, exists, running)
			if err := manager.validateConfigPlan(plan, running); err != nil {
				return plan, err
			}
		} else if err := manager.validateConfigPlan(plan, false); err != nil {
			return plan, err
		} else {
			plan.RuntimeAction = "metadata-only"
		}
		return plan, nil
	}

	lock, err := manager.operationLock("config-set")
	if err != nil {
		return ConfigPlan{}, err
	}
	defer releaseLock(lock, &operationErr)

	if err := manager.validateConfigLayout(false, false); err != nil {
		return ConfigPlan{}, err
	}
	current, err := manager.ConfigStore.Load()
	if err != nil {
		return ConfigPlan{}, err
	}
	plan, err = planConfigPatch(current, patch)
	if err != nil || len(plan.Changes) == 0 {
		return plan, err
	}
	var values secrets.Values
	var composeBefore managedComposeSnapshot

	running := false
	exists := false
	if plan.RequiresComposeUpdate {
		if err := manager.validateConfigLayout(true, true); err != nil {
			return plan, err
		}
		values, err = manager.SecretStore.Load()
		if err != nil {
			return plan, err
		}
		composeBefore, err = captureManagedCompose(manager.Paths)
		if err != nil {
			return plan, err
		}
		if err := manager.Docker.CheckDaemon(ctx); err != nil {
			return plan, err
		}
		running, err = manager.Docker.ServiceRunningStatus(ctx)
		if err != nil {
			return plan, fmt.Errorf("inspect running Rakazo API: %w", err)
		}
		if err := manager.validateConfigPlan(plan, running); err != nil {
			return plan, err
		}
		exists, err = manager.Docker.ServiceExists(ctx)
		if err != nil {
			return plan, fmt.Errorf("inspect existing Rakazo API: %w", err)
		}
		if running {
			if err := manager.verifyContainerBinds(ctx); err != nil {
				return plan, fmt.Errorf("pre-change mount check failed: %w", err)
			}
		}
		setConfigRuntime(&plan, exists, running)
	} else if err := manager.validateConfigPlan(plan, false); err != nil {
		return plan, err
	} else {
		plan.RuntimeAction = "metadata-only"
	}

	if err := manager.ConfigStore.Save(plan.After); err != nil {
		return plan, err
	}
	rollbackMetadata := func() error {
		var rollbackErrors []error
		if saveErr := manager.ConfigStore.Save(plan.Before); saveErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous metadata: %w", saveErr))
		}
		if composeErr := restoreManagedCompose(composeBefore); composeErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous Compose: %w", composeErr))
		}
		return errors.Join(rollbackErrors...)
	}
	if plan.RequiresComposeUpdate {
		if err := manager.Generator.Prepare(plan.After, values, false); err != nil {
			rollbackErr := rollbackMetadata()
			return plan, configApplyError("update Compose", err, rollbackErr)
		}
		if err := manager.Docker.ValidateCompose(ctx); err != nil {
			rollbackErr := rollbackMetadata()
			return plan, configApplyError("validate updated Compose", err, rollbackErr)
		}
	}

	if running && plan.RequiresRecreate {
		if err := manager.Docker.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300", "--force-recreate", "--remove-orphans"); err != nil {
			rollbackErr := manager.rollbackConfigRuntime(ctx, plan.Before, composeBefore, true)
			return plan, configApplyError("recreate Rakazo with updated configuration", err, rollbackErr)
		}
		if err := manager.verifyContainerBinds(ctx); err != nil {
			rollbackErr := manager.rollbackConfigRuntime(ctx, plan.Before, composeBefore, true)
			return plan, configApplyError("verify bind mounts after configuration change", err, rollbackErr)
		}
		if _, err := manager.waitForAPI(ctx, apiReadyTimeout); err != nil {
			rollbackErr := manager.rollbackConfigRuntime(ctx, plan.Before, composeBefore, true)
			return plan, configApplyError("verify API after configuration change", err, rollbackErr)
		}
	} else if exists && plan.RequiresRecreate {
		if err := manager.Docker.Compose(ctx, false, "up", "--no-start", "--force-recreate", "--remove-orphans"); err != nil {
			rollbackErr := manager.rollbackConfigRuntime(ctx, plan.Before, composeBefore, false)
			return plan, configApplyError("recreate stopped Rakazo stack with updated configuration", err, rollbackErr)
		}
	}
	_ = manager.StateStore.Log("config-set", fmt.Sprintf("changes=%d recreated=%t running=%t result=success", len(plan.Changes), exists && plan.RequiresRecreate, running))
	return plan, nil
}

func setConfigRuntime(plan *ConfigPlan, exists, running bool) {
	plan.RuntimeInspected = true
	plan.ServiceExists = exists
	plan.ServiceRunning = running
	switch {
	case running:
		plan.RuntimeAction = "recreate-running-service-and-verify"
	case exists:
		plan.RuntimeAction = "recreate-stopped-service"
	default:
		plan.RuntimeAction = "update-compose-only"
	}
}

func (manager *Manager) validateConfigLayout(requireSecrets, requireCompose bool) error {
	for _, item := range []struct {
		label string
		path  string
	}{
		{"instance root", manager.Paths.Root},
		{"manager directory", manager.Paths.Manager},
	} {
		info, err := os.Lstat(item.path)
		if err != nil {
			return fmt.Errorf("inspect %s for configuration: %w", item.label, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refuse configuration change because %s is not a real directory: %s", item.label, item.path)
		}
	}
	if filepath.Dir(filepath.Clean(manager.Paths.Manager)) != filepath.Clean(manager.Paths.Root) {
		return fmt.Errorf("refuse configuration change because manager directory is outside the instance root")
	}
	if err := requireRealConfigFile("instance metadata", manager.Paths.Config); err != nil {
		return err
	}
	if requireSecrets {
		if err := requireRealConfigFile("instance secrets", manager.Paths.Secrets); err != nil {
			return err
		}
	}
	if requireCompose {
		composePath := manager.Paths.Compose
		if _, err := os.Lstat(composePath); errors.Is(err, os.ErrNotExist) {
			composePath = manager.Paths.LegacyCompose()
		}
		if err := requireRealConfigFile("Compose file", composePath); err != nil {
			return err
		}
	}
	return nil
}

func requireRealConfigFile(label, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s for configuration: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refuse configuration change because %s is not a real regular file: %s", label, path)
	}
	return nil
}

func (manager *Manager) validateConfigPlan(plan ConfigPlan, ownsCurrentListeners bool) error {
	if plan.Before.Name != plan.After.Name {
		schedulePath := manager.Paths.Schedule
		if info, err := os.Lstat(schedulePath); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("cannot change the instance name while backup schedule state at %s is not a regular file", schedulePath)
			}
			return fmt.Errorf("cannot change the instance name while a backup schedule exists; run schedule remove first, change the name, then set the schedule again")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect backup schedule before changing the instance name: %w", err)
		}
		if err := validateUniqueInstanceName(manager.Paths.Root, plan.After.Name); err != nil {
			return err
		}
	}
	bindChanged := plan.Before.BindAddress != plan.After.BindAddress
	portsChanged := bindChanged ||
		plan.Before.WebPort != plan.After.WebPort ||
		plan.Before.APIPort != plan.After.APIPort ||
		plan.Before.PostgresPort != plan.After.PostgresPort
	if !portsChanged {
		return nil
	}
	if conflicts := ports.Conflicts(manager.Paths.Root, plan.After.BindAddress, plan.After.WebPort, plan.After.APIPort, plan.After.PostgresPort); len(conflicts) > 0 {
		return fmt.Errorf("requested ports conflict: %v", conflicts)
	}
	owned := map[int]bool{
		plan.Before.WebPort:      true,
		plan.Before.APIPort:      true,
		plan.Before.PostgresPort: true,
	}
	exclude := map[int]bool{
		plan.After.WebPort:      true,
		plan.After.APIPort:      true,
		plan.After.PostgresPort: true,
	}
	for _, item := range []struct {
		before int
		after  int
		role   string
	}{
		{plan.Before.WebPort, plan.After.WebPort, "web"},
		{plan.Before.APIPort, plan.After.APIPort, "API"},
		{plan.Before.PostgresPort, plan.After.PostgresPort, "Postgres"},
	} {
		if item.before == item.after && !bindChanged {
			continue
		}
		if ownsCurrentListeners && owned[item.after] {
			continue
		}
		others := map[int]bool{}
		for port := range exclude {
			if port != item.after {
				others[port] = true
			}
		}
		if err := ports.Check(manager.Paths.Root, plan.After.BindAddress, item.after, others); err != nil {
			return fmt.Errorf("%s port: %w", item.role, err)
		}
	}
	return nil
}

func validateUniqueInstanceName(root, name string) error {
	parent := filepath.Dir(filepath.Clean(root))
	entries, err := os.ReadDir(parent)
	if err != nil {
		return fmt.Errorf("inspect sibling instance names: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(parent, entry.Name())
		if filepath.Clean(candidate) == filepath.Clean(root) {
			continue
		}
		paths, pathsErr := stack.NewPaths(candidate)
		if pathsErr != nil {
			continue
		}
		managerInfo, statErr := os.Lstat(paths.Manager)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect sibling manager directory %s: %w", paths.Manager, statErr)
		}
		if managerInfo.Mode()&os.ModeSymlink != 0 || !managerInfo.IsDir() {
			continue
		}
		configInfo, statErr := os.Lstat(paths.Config)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect sibling instance metadata %s: %w", paths.Config, statErr)
		}
		if configInfo.Mode()&os.ModeSymlink != 0 || !configInfo.Mode().IsRegular() {
			continue
		}
		cfg, loadErr := (config.Store{Paths: paths}).Load()
		if loadErr != nil {
			continue
		}
		if cfg.Name == name {
			return fmt.Errorf("instance name %q is already used at %s", name, candidate)
		}
	}
	return nil
}

func (manager *Manager) rollbackConfigRuntime(ctx context.Context, cfg config.Config, composeBefore managedComposeSnapshot, wasRunning bool) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), dashboardReadyTimeout)
	defer cancel()
	var rollbackErrors []error
	if err := manager.ConfigStore.Save(cfg); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous metadata: %w", err))
	}
	if err := restoreManagedCompose(composeBefore); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("restore previous Compose: %w", err))
		return errors.Join(rollbackErrors...)
	}
	if wasRunning {
		if err := manager.Docker.Compose(rollbackCtx, false, "up", "-d", "--wait", "--wait-timeout", "300", "--force-recreate", "--remove-orphans"); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("recreate previous container behavior: %w", err))
			return errors.Join(rollbackErrors...)
		}
		if err := manager.verifyContainerBinds(rollbackCtx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("verify restored bind mounts: %w", err))
			return errors.Join(rollbackErrors...)
		}
		if _, err := manager.waitForAPI(rollbackCtx, apiReadyTimeout); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("verify restored API health: %w", err))
			return errors.Join(rollbackErrors...)
		}
	} else if err := manager.Docker.Compose(rollbackCtx, false, "up", "--no-start", "--force-recreate", "--remove-orphans"); err != nil {
		rollbackErrors = append(rollbackErrors, fmt.Errorf("recreate previous stopped container behavior: %w", err))
	}
	return errors.Join(rollbackErrors...)
}

type managedFileSnapshot struct {
	path    string
	exists  bool
	content []byte
	mode    os.FileMode
}

type managedComposeSnapshot struct {
	files []managedFileSnapshot
}

func captureManagedCompose(paths stack.Paths) (managedComposeSnapshot, error) {
	snapshot := managedComposeSnapshot{}
	for _, candidate := range []string{paths.Compose, paths.LegacyCompose()} {
		item := managedFileSnapshot{path: candidate}
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			snapshot.files = append(snapshot.files, item)
			continue
		}
		if err != nil {
			return managedComposeSnapshot{}, fmt.Errorf("inspect Compose rollback source %s: %w", candidate, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return managedComposeSnapshot{}, fmt.Errorf("Compose rollback source is not a regular file: %s", candidate)
		}
		content, err := os.ReadFile(candidate)
		if err != nil {
			return managedComposeSnapshot{}, fmt.Errorf("read Compose rollback source %s: %w", candidate, err)
		}
		item.exists = true
		item.content = content
		item.mode = info.Mode().Perm()
		snapshot.files = append(snapshot.files, item)
	}
	return snapshot, nil
}

func restoreManagedCompose(snapshot managedComposeSnapshot) error {
	for _, item := range snapshot.files {
		if item.exists {
			if err := fsutil.AtomicWriteFile(item.path, item.content, item.mode); err != nil {
				return fmt.Errorf("restore Compose file %s: %w", item.path, err)
			}
			continue
		}
		if err := removeKnownFile(item.path); err != nil {
			return err
		}
	}
	return nil
}

func configApplyError(stage string, applyErr, rollbackErr error) error {
	if rollbackErr != nil {
		return fmt.Errorf("%s: %w; restoring previous configuration also failed: %v", stage, applyErr, rollbackErr)
	}
	return fmt.Errorf("%s: %w; previous configuration and container behavior were restored", stage, applyErr)
}

type DecommissionOptions struct {
	DeleteData        bool `json:"delete_data"`
	DeleteWorkspace   bool `json:"delete_workspace"`
	DeleteBackups     bool `json:"delete_backups"`
	DeleteOverride    bool `json:"delete_override"`
	AllowDataDeletion bool `json:"allow_data_deletion"`
	DryRun            bool `json:"dry_run"`
}

type DecommissionPlan struct {
	Root                 string   `json:"root"`
	Remove               []string `json:"remove"`
	Retain               []string `json:"retain"`
	RemoveContainers     bool     `json:"remove_containers"`
	RemoveBackupSchedule bool     `json:"remove_backup_schedule"`
	DeletesDurableData   bool     `json:"deletes_durable_data"`
}

func (manager *Manager) PlanDecommission(options DecommissionOptions) (DecommissionPlan, error) {
	if err := manager.validateDecommissionLayout(); err != nil {
		return DecommissionPlan{}, err
	}
	if err := manager.RequireInstalled(); err != nil {
		return DecommissionPlan{}, err
	}
	deletesDurable := options.DeleteData || options.DeleteWorkspace || options.DeleteBackups || options.DeleteOverride
	if deletesDurable && !options.AllowDataDeletion {
		return DecommissionPlan{}, fmt.Errorf("refuse durable-data deletion without AllowDataDeletion")
	}
	schedulePresent := false
	if info, err := os.Lstat(manager.Paths.Schedule); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return DecommissionPlan{}, fmt.Errorf("backup schedule state is not a real regular file: %s", manager.Paths.Schedule)
		}
		schedulePresent = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return DecommissionPlan{}, fmt.Errorf("inspect backup schedule state: %w", err)
	}
	plan := DecommissionPlan{
		Root:                 manager.Paths.Root,
		RemoveContainers:     true,
		RemoveBackupSchedule: schedulePresent,
		DeletesDurableData:   deletesDurable,
	}
	for _, candidate := range []string{
		manager.Paths.Compose,
		manager.Paths.LegacyCompose(),
		manager.Paths.State,
		manager.Paths.OperationsLog,
		manager.Paths.Schedule,
		manager.Paths.ScheduleLog,
		manager.Paths.Secrets,
		manager.Paths.Config,
	} {
		if _, err := os.Lstat(candidate); err == nil {
			plan.Remove = append(plan.Remove, candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			return DecommissionPlan{}, fmt.Errorf("inspect decommission target %s: %w", candidate, err)
		}
	}
	for _, item := range []struct {
		path   string
		remove bool
	}{
		{manager.Paths.Data, options.DeleteData},
		{manager.Paths.Workspace, options.DeleteWorkspace},
		{manager.Paths.Backups, options.DeleteBackups},
		{manager.Paths.ComposeOverride(), options.DeleteOverride},
	} {
		if _, err := os.Lstat(item.path); err == nil {
			if item.remove {
				plan.Remove = append(plan.Remove, item.path)
			} else {
				plan.Retain = append(plan.Retain, item.path)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return DecommissionPlan{}, fmt.Errorf("inspect decommission path %s: %w", item.path, err)
		}
	}
	unknown, err := manager.unknownManagerEntries()
	if err != nil {
		return DecommissionPlan{}, err
	}
	plan.Retain = append(plan.Retain, unknown...)
	sort.Strings(plan.Remove)
	sort.Strings(plan.Retain)
	return plan, nil
}

func (manager *Manager) validateDecommissionLayout() error {
	for _, item := range []struct {
		label string
		path  string
	}{
		{"instance root", manager.Paths.Root},
		{"manager directory", manager.Paths.Manager},
	} {
		info, err := os.Lstat(item.path)
		if err != nil {
			return fmt.Errorf("inspect %s for decommission: %w", item.label, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refuse decommission because %s is not a real directory: %s", item.label, item.path)
		}
	}
	if filepath.Dir(filepath.Clean(manager.Paths.Manager)) != filepath.Clean(manager.Paths.Root) {
		return fmt.Errorf("refuse decommission because manager directory is outside the instance root")
	}
	if err := requireRealDecommissionFile("instance metadata", manager.Paths.Config); err != nil {
		return err
	}
	composePath := manager.Paths.Compose
	if _, err := os.Lstat(composePath); errors.Is(err, os.ErrNotExist) {
		composePath = manager.Paths.LegacyCompose()
	}
	if err := requireRealDecommissionFile("Compose file", composePath); err != nil {
		return err
	}
	return nil
}

func requireRealDecommissionFile(label, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s for decommission: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refuse decommission because %s is not a real regular file: %s", label, path)
	}
	return nil
}

func (manager *Manager) Decommission(ctx context.Context, options DecommissionOptions) (plan DecommissionPlan, operationErr error) {
	plan, err := manager.PlanDecommission(options)
	if err != nil || options.DryRun {
		return plan, err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return plan, err
	}
	lock, err := manager.operationLock("decommission")
	if err != nil {
		return plan, err
	}
	lockReleased := false
	defer func() {
		if !lockReleased {
			releaseLock(lock, &operationErr)
		}
	}()

	plan, err = manager.PlanDecommission(options)
	if err != nil {
		return plan, err
	}
	scheduleStore, err := manager.newCrontabScheduleStore()
	if err != nil {
		return plan, err
	}
	previousSchedule, hadSchedule, err := scheduleStore.Show(ctx)
	if err != nil {
		return plan, fmt.Errorf("inspect owned backup schedule: %w", err)
	}
	removedSchedule, err := scheduleStore.Remove(ctx)
	if err != nil {
		return plan, fmt.Errorf("remove owned backup schedule: %w", err)
	}
	restoreSchedule := func(stageErr error) error {
		if !removedSchedule || !hadSchedule {
			return stageErr
		}
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if restoreErr := scheduleStore.Set(restoreCtx, previousSchedule); restoreErr != nil {
			return fmt.Errorf("%w; restoring the owned backup schedule also failed: %v", stageErr, restoreErr)
		}
		return fmt.Errorf("%w; the owned backup schedule was restored", stageErr)
	}
	if err := manager.Docker.Compose(ctx, false, "down", "--remove-orphans"); err != nil {
		return plan, restoreSchedule(fmt.Errorf("remove instance containers: %w", err))
	}

	ordered := make([]string, 0, len(plan.Remove))
	for _, target := range plan.Remove {
		if target == manager.Paths.Data || target == manager.Paths.Workspace || target == manager.Paths.Backups || target == manager.Paths.ComposeOverride() {
			ordered = append(ordered, target)
		}
	}
	for _, target := range plan.Remove {
		if target != manager.Paths.Config && target != manager.Paths.Data && target != manager.Paths.Workspace && target != manager.Paths.Backups && target != manager.Paths.ComposeOverride() {
			ordered = append(ordered, target)
		}
	}
	for _, target := range plan.Remove {
		if target == manager.Paths.Config {
			ordered = append(ordered, target)
		}
	}
	removedAnyFile := false
	fileRemovalError := func(err error) error {
		if !removedAnyFile {
			return restoreSchedule(err)
		}
		return fmt.Errorf("%w; the owned backup schedule remains removed because decommission partially completed", err)
	}
	for _, target := range ordered {
		_, statErr := os.Lstat(target)
		existed := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return plan, fileRemovalError(fmt.Errorf("inspect decommission target %s immediately before removal: %w", target, statErr))
		}
		if target == manager.Paths.Data || target == manager.Paths.Workspace || target == manager.Paths.Backups || target == manager.Paths.ComposeOverride() {
			if err := removeExactInstanceChild(manager.Paths.Root, target); err != nil {
				return plan, fileRemovalError(err)
			}
			removedAnyFile = removedAnyFile || existed
			continue
		}
		if err := removeKnownFile(target); err != nil {
			return plan, fileRemovalError(err)
		}
		removedAnyFile = removedAnyFile || existed
	}
	if err := lock.Release(); err != nil {
		return plan, err
	}
	lockReleased = true
	if err := os.Remove(manager.Paths.Manager); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
		return plan, fmt.Errorf("remove empty manager directory: %w", err)
	}
	return plan, nil
}

func (manager *Manager) unknownManagerEntries() ([]string, error) {
	entries, err := os.ReadDir(manager.Paths.Manager)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list manager directory: %w", err)
	}
	known := map[string]bool{
		filepath.Base(manager.Paths.Config):        true,
		filepath.Base(manager.Paths.Secrets):       true,
		filepath.Base(manager.Paths.State):         true,
		filepath.Base(manager.Paths.OperationsLog): true,
		filepath.Base(manager.Paths.Lock):          true,
		"schedule.json":                            true,
		"schedule.log":                             true,
	}
	var result []string
	for _, entry := range entries {
		if !known[entry.Name()] {
			result = append(result, filepath.Join(manager.Paths.Manager, entry.Name()))
		}
	}
	return result, nil
}

func removeKnownFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect generated file %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("refuse to recursively delete generated-file path that is a directory: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove generated file %s: %w", path, err)
	}
	return nil
}

func removeExactInstanceChild(root, target string) error {
	cleanRoot := filepath.Clean(root)
	cleanTarget := filepath.Clean(target)
	if filepath.Dir(cleanTarget) != cleanRoot {
		return fmt.Errorf("refuse deletion outside immediate instance child: %s", cleanTarget)
	}
	if err := os.RemoveAll(cleanTarget); err != nil {
		return fmt.Errorf("remove explicitly selected path %s: %w", cleanTarget, err)
	}
	return nil
}
