package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/manager"
	"github.com/nicolaeser/RakazoManager/internal/ports"
	"github.com/nicolaeser/RakazoManager/internal/ui"
)

func (rt runtime) withInterrupt(ctx context.Context, fn func(context.Context) error) error {
	opCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := fn(opCtx)
	if command.IsInterrupted(err) {
		return fmt.Errorf("cancelled")
	}
	return err
}

func (rt runtime) mainMenu(ctx context.Context) error {
	if !rt.manager.IsInstalled() {
		rt.ui.Banner(rt.app.Build.Version, rt.paths.Root)
		folder, err := rt.ui.Prompt("Installation folder", rt.paths.Root)
		if err != nil {
			return err
		}
		rebound, err := rt.app.runtime(folder, rt.ui)
		if err != nil {
			return err
		}
		rt = rebound
		installNow, err := rt.ui.Confirm("Install Rakazo here?", true)
		if err != nil {
			return err
		}
		if !installNow {
			return nil
		}
		bindAll, err := rt.ui.Confirm("Publish the web UI on every interface (0.0.0.0)? API and Postgres stay on 127.0.0.1. Default is localhost-only.", false)
		if err != nil {
			return err
		}
		bindAddress := config.DefaultBindAddress
		if bindAll {
			if err := rt.ui.RequirePhrase(
				"Publishing on 0.0.0.0 exposes the web UI on every interface. API and Postgres stay on 127.0.0.1. Protect with firewall/VPN/proxy.",
				"BIND-ALL",
			); err != nil {
				return err
			}
			bindAddress = config.PublicBindAddress
		}
		webPort, apiPort, postgresPort, err := rt.promptInstallPorts(bindAddress)
		if err != nil {
			return err
		}
		origin, err := rt.ui.Prompt("Public origin (IP, domain, or URL; blank = localhost)", "")
		if err != nil {
			return err
		}
		rebuildOnStart, err := rt.ui.Confirm("Rebuild docker-compose.yml on every start?", false)
		if err != nil {
			return err
		}
		startNow, err := rt.ui.Confirm("Start the stack now?", true)
		if err != nil {
			return err
		}
		err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
			result, installErr := rt.manager.Install(opCtx, manager.InstallOptions{
				WebPort:                  webPort,
				APIPort:                  apiPort,
				PostgresPort:             postgresPort,
				BindAll:                  bindAll,
				Origin:                   origin,
				Pull:                     true,
				Start:                    startNow,
				RebuildComposeOnStart:    rebuildOnStart,
				HasRebuildComposeOnStart: true,
			})
			if installErr != nil {
				return installErr
			}
			if startNow {
				rt.ui.Success("Rakazo installed and started")
			} else {
				rt.ui.Success("Rakazo installed (not started)")
				rt.ui.Info("Adjust docker-compose.yml if needed, then run: rakazo-manager start %q", rt.paths.Root)
			}
			rt.printInstallSummary(result.Config)
			return nil
		})
		if err != nil {
			return err
		}
	}

	for {
		rt.ui.Banner(rt.app.Build.Version, rt.paths.Root)
		choice, err := rt.ui.Menu("Operations", []ui.Item{
			{Key: "1", Label: "Start", Description: "Create or start the stack"},
			{Key: "2", Label: "Stop", Description: "Keep every persistent file"},
			{Key: "3", Label: "Restart"},
			{Key: "4", Label: "Status", Description: "Show image, ports, paths, and container state"},
			{Key: "5", Label: "Logs", Description: "Follow the latest container output"},
			{Key: "6", Label: "Shell", Description: "Interactive bash inside the API container"},
			{Key: "7", Label: "Open URL", Description: "Show the local web and health URLs"},
			{Key: "8", Label: "Backup", Description: "Create and verify a Postgres + data archive"},
			{Key: "9", Label: "Restore", Description: "Make a safety backup, then restore an archive"},
			{Key: "10", Label: "Update", Description: "Backup, pull, recreate, verify, auto-rollback"},
			{Key: "11", Label: "Rollback", Description: "Use the previously recorded image"},
			{Key: "12", Label: "Check image update", Description: "Registry digest check without pull or recreate"},
			{Key: "13", Label: "Administration", Description: "Config, schedule, import, decommission, siblings, doctor"},
			{Key: "0", Label: "Exit"},
		})
		if err != nil {
			return err
		}

		switch choice {
		case "1":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				return rt.withSuccess("Rakazo started", rt.manager.Start(opCtx, manager.StartOptions{}))
			})
		case "2":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				return rt.withSuccess("Rakazo stopped; persistent data was retained", rt.manager.Stop(opCtx))
			})
		case "3":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				return rt.withSuccess("Rakazo restarted", rt.manager.Restart(opCtx))
			})
		case "4":
			err = rt.withInterrupt(ctx, rt.statusCommand)
		case "5":
			err = rt.manager.Logs(ctx, 100)
		case "6":
			err = rt.manager.Shell(ctx)
		case "7":
			err = rt.dashboardCommand()
		case "8":
			err = rt.withInterrupt(ctx, rt.menuBackup)
		case "9":
			err = rt.withInterrupt(ctx, rt.menuRestore)
		case "10":
			err = rt.withInterrupt(ctx, rt.menuUpdate)
		case "11":
			err = rt.ui.RequirePhrase("Rollback recreates Rakazo with the previous image.", "ROLLBACK")
			if err == nil {
				err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
					return rt.withSuccess("Previous image restored", rt.manager.Rollback(opCtx))
				})
			}
		case "12":
			err = rt.withInterrupt(ctx, rt.menuUpdateCheck)
		case "13":
			err = rt.administrationMenu(ctx)
		case "0":
			return nil
		default:
			err = fmt.Errorf("unknown selection %q", choice)
		}
		if err != nil {
			rt.ui.Failure("%v", err)
		}
		if choice != "5" && choice != "6" {
			rt.ui.Pause()
		}
	}
}

func (rt runtime) administrationMenu(ctx context.Context) error {
	for {
		rt.ui.Banner(rt.app.Build.Version, rt.paths.Root)
		choice, err := rt.ui.Menu("Administration", []ui.Item{
			{Key: "1", Label: "Run doctor", Description: "Validate storage, mounts, Docker, and API health"},
			{Key: "2", Label: "Clear stale lock", Description: "Remove leftover operation lock if its process is gone"},
			{Key: "3", Label: "Show configuration", Description: "Name, image, ports, bind address, rebuild policy"},
			{Key: "4", Label: "Change configuration", Description: "Safely update managed fields with dry-run support"},
			{Key: "5", Label: "List sibling instances", Description: "Discover managed installs under the parent folder"},
			{Key: "6", Label: "Scheduled backups", Description: "Install, show, remove, or run the backup schedule"},
			{Key: "7", Label: "Import instance export", Description: "Recover a disaster-recovery ZIP into a new folder"},
			{Key: "8", Label: "Export complete instance", Description: "Data, secrets, and recovery metadata"},
			{Key: "9", Label: "Prune automatic backups", Description: "Manual backups and instance exports are retained"},
			{Key: "10", Label: "Set OpenRouter key", Description: "Store OPENROUTER_API_KEY and recreate API/worker/web"},
			{Key: "11", Label: "Repair managed files", Description: "Regenerate Compose without changing data or ports"},
			{Key: "12", Label: "Toggle network bind", Description: "Switch between 127.0.0.1 and 0.0.0.0 with rollback"},
			{Key: "13", Label: "Decommission instance", Description: "Remove containers/files; retain durable data by default"},
			{Key: "14", Label: "Explain storage layout", Description: "./pg, ./data, ./backups and what updates preserve"},
			{Key: "0", Label: "Back"},
		})
		if err != nil {
			return err
		}
		switch choice {
		case "1":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				report := rt.manager.Doctor(opCtx)
				rt.printDoctor(report)
				if !report.Healthy() {
					return fmt.Errorf("one or more doctor checks failed")
				}
				return nil
			})
		case "2":
			err = rt.menuClearStaleLock()
		case "3":
			err = rt.menuShowConfig()
		case "4":
			err = rt.withInterrupt(ctx, rt.menuChangeConfig)
		case "5":
			err = rt.menuListSiblingInstances()
		case "6":
			err = rt.withInterrupt(ctx, rt.menuSchedule)
		case "7":
			err = rt.withInterrupt(ctx, rt.menuImportInstance)
		case "8":
			err = rt.withInterrupt(ctx, rt.menuExportInstance)
		case "9":
			err = rt.menuPruneBackups()
		case "10":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				key, promptErr := rt.ui.Prompt("OpenRouter API key (blank to clear)", "")
				if promptErr != nil {
					return promptErr
				}
				if err := rt.manager.SetOpenRouterAPIKey(opCtx, key); err != nil {
					return err
				}
				rt.ui.Success("OpenRouter key updated")
				return nil
			})
		case "11":
			err = rt.withInterrupt(ctx, func(opCtx context.Context) error {
				if _, repairErr := rt.manager.Install(opCtx, manager.InstallOptions{}); repairErr != nil {
					return repairErr
				}
				rt.ui.Success("Managed files repaired; persistent data was untouched")
				return nil
			})
		case "12":
			err = rt.withInterrupt(ctx, rt.menuToggleBind)
		case "13":
			err = rt.withInterrupt(ctx, rt.menuDecommission)
		case "14":
			rt.printStorageLayout()
			err = nil
		case "0":
			return nil
		default:
			err = fmt.Errorf("unknown selection %q", choice)
		}
		if err != nil {
			rt.ui.Failure("%v", err)
		}
		rt.ui.Pause()
	}
}

func (rt runtime) menuPruneBackups() error {
	keep, err := rt.ui.PromptInt("Automatic backups to keep", 10)
	if err != nil {
		return err
	}
	files, err := rt.manager.BackupsToPrune(keep)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		rt.ui.Success("Nothing to prune")
		return nil
	}
	for _, file := range files {
		fmt.Fprintln(rt.app.Out, "  "+file)
	}
	confirmed, err := rt.ui.Confirm(fmt.Sprintf("Delete %d old automatic backup(s)?", len(files)), false)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	deleted, err := rt.manager.PruneAutomaticBackups(keep)
	if err != nil {
		return err
	}
	rt.ui.Success("Deleted %d old automatic backup(s); manual backups were retained", len(deleted))
	return nil
}

func (rt runtime) menuExportInstance(ctx context.Context) error {
	includeWorkspace, err := rt.ui.Confirm("Include workspace files in the export?", true)
	if err != nil {
		return err
	}
	confirmed, err := rt.ui.Confirm("The export contains instance secrets. Create it now?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	archive, err := rt.manager.ExportInstance(ctx, includeWorkspace)
	if err != nil {
		return err
	}
	rt.ui.Success("Instance export created and verified: %s", archive)
	rt.ui.Warn("Store this archive securely because it contains instance secrets.")
	return nil
}

func (rt runtime) menuBackup(ctx context.Context) error {
	label, err := rt.ui.Prompt("Backup label", "manual")
	if err != nil {
		return err
	}
	archive, err := rt.manager.Backup(ctx, label)
	if err != nil {
		return err
	}
	rt.ui.Success("Backup created and verified: %s", archive)
	return nil
}

func (rt runtime) menuRestore(ctx context.Context) error {
	if err := rt.listBackups(); err != nil {
		return err
	}
	archive, err := rt.ui.Prompt("Backup filename or absolute path", "")
	if err != nil {
		return err
	}
	if err := rt.ui.RequirePhrase("Restore replaces Rakazo configuration and user data. A safety backup is created first.", "RESTORE"); err != nil {
		return err
	}
	if err := rt.manager.Restore(ctx, archive); err != nil {
		return err
	}
	rt.ui.Success("Restore completed")
	return nil
}

func (rt runtime) menuUpdate(ctx context.Context) error {
	rt.ui.Info("Update pulls a new image and recreates the app containers only.")
	rt.ui.Info("Host folders ./pg, ./data, ./workspace, and ./backups stay bind-mounted and are not deleted.")
	confirmed, err := rt.ui.Confirm("Create a backup, pull the newest image, and update Rakazo?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return nil
	}
	return rt.withSuccess("Update completed and verified; host data was preserved", rt.manager.Update(ctx))
}

func (rt runtime) menuToggleBind(ctx context.Context) error {
	cfg, err := rt.manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	currentlyPublic := cfg.BindAddress == config.PublicBindAddress
	rt.ui.Section("Network bind")
	rt.ui.KeyValue("Current", cfg.BindAddress)
	if currentlyPublic {
		rt.ui.Warn("The web UI is published on every host interface (0.0.0.0). API and Postgres stay on 127.0.0.1.")
	} else {
		rt.ui.Info("The web UI is localhost-only (127.0.0.1). Use an SSH tunnel or a host reverse proxy for remote access.")
	}

	var public bool
	if currentlyPublic {
		confirmed, confErr := rt.ui.Confirm("Switch to localhost-only (127.0.0.1)?", true)
		if confErr != nil {
			return confErr
		}
		if !confirmed {
			return nil
		}
		public = false
	} else {
		if err := rt.ui.RequirePhrase(
			"Publishing on 0.0.0.0 exposes the web UI on every interface. API and Postgres stay on 127.0.0.1. Protect with firewall/VPN/proxy.",
			"BIND-ALL",
		); err != nil {
			return err
		}
		public = true
	}
	if err := rt.manager.SetBindAddress(ctx, public); err != nil {
		return err
	}
	cfg, err = rt.manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	rt.ui.Success("Bind address is now %s", cfg.BindAddress)
	rt.ui.KeyValue("Web", fmt.Sprintf("http://127.0.0.1:%d", cfg.WebPort))
	return nil
}

func (rt runtime) menuClearStaleLock() error {
	cleared, detail, err := rt.manager.ClearStaleLock()
	if err != nil {
		return err
	}
	if cleared {
		rt.ui.Success("Cleared stale operation lock")
		rt.ui.KeyValue("Previous lock", detail)
		return nil
	}
	rt.ui.Success("No operation lock needed clearing")
	rt.ui.Info("%s", detail)
	return nil
}

func (rt runtime) menuShowConfig() error {
	cfg, err := rt.manager.ShowConfig()
	if err != nil {
		return err
	}
	rt.ui.Section("Managed configuration")
	rt.ui.KeyValue("Name", cfg.Name)
	rt.ui.KeyValue("Image", cfg.Image)
	if cfg.PinnedImage != "" {
		rt.ui.KeyValue("Pinned image", cfg.PinnedImage)
	}
	rt.ui.KeyValue("Web port", cfg.WebPort)
	rt.ui.KeyValue("API port", cfg.APIPort)
	rt.ui.KeyValue("Postgres port", cfg.PostgresPort)
	rt.ui.KeyValue("Bind address", cfg.BindAddress)
	rt.ui.KeyValue("Origin", cfg.PublicOrigin())
	rt.ui.KeyValue("Rebuild on start", cfg.RebuildComposeOnStart)
	return nil
}

func (rt runtime) menuChangeConfig(ctx context.Context) error {
	cfg, err := rt.manager.ShowConfig()
	if err != nil {
		return err
	}
	rt.ui.Section("Change configuration")
	rt.ui.Info("Leave a prompt blank to keep the current value.")
	name, err := rt.ui.Prompt("Name", cfg.Name)
	if err != nil {
		return err
	}
	image, err := rt.ui.Prompt("Image", cfg.Image)
	if err != nil {
		return err
	}
	webPort, err := rt.ui.PromptInt("Web port", cfg.WebPort)
	if err != nil {
		return err
	}
	apiPort, err := rt.ui.PromptInt("API port", cfg.APIPort)
	if err != nil {
		return err
	}
	postgresPort, err := rt.ui.PromptInt("Postgres port", cfg.PostgresPort)
	if err != nil {
		return err
	}
	bindAddress, err := rt.ui.Prompt("Bind address (127.0.0.1 or 0.0.0.0)", cfg.BindAddress)
	if err != nil {
		return err
	}
	origin, err := rt.ui.Prompt("Public origin (IP, domain, or URL)", cfg.Origin)
	if err != nil {
		return err
	}
	rebuild, err := rt.ui.Confirm("Rebuild docker-compose.yml on every start?", cfg.RebuildComposeOnStart)
	if err != nil {
		return err
	}
	patch := manager.ConfigPatch{
		Name:                  &name,
		Image:                 &image,
		WebPort:               &webPort,
		APIPort:               &apiPort,
		PostgresPort:          &postgresPort,
		BindAddress:           &bindAddress,
		Origin:                &origin,
		RebuildComposeOnStart: &rebuild,
	}
	dryRun, err := rt.ui.Confirm("Dry-run first (validate without applying)?", true)
	if err != nil {
		return err
	}
	plan, err := rt.manager.ApplyConfigPatch(ctx, patch, dryRun)
	if err != nil {
		return err
	}
	if len(plan.Changes) == 0 {
		rt.ui.Success("No configuration changes requested")
		return nil
	}
	rt.ui.Section("Configuration plan")
	for _, change := range plan.Changes {
		rt.ui.Info("%s: %v → %v", change.Field, change.Before, change.After)
	}
	if plan.RuntimeAction != "" {
		rt.ui.KeyValue("Runtime action", plan.RuntimeAction)
	}
	if dryRun {
		rt.ui.Success("Dry run complete; no changes were applied")
		applyNow, confErr := rt.ui.Confirm("Apply this plan now?", false)
		if confErr != nil {
			return confErr
		}
		if !applyNow {
			return nil
		}
		plan, err = rt.manager.ApplyConfigPatch(ctx, patch, false)
		if err != nil {
			return err
		}
	}
	rt.ui.Success("Configuration updated (%d change(s))", len(plan.Changes))
	return nil
}

func (rt runtime) menuListSiblingInstances() error {
	parent := filepath.Dir(rt.paths.Root)
	discovery, err := manager.DiscoverInstances(parent)
	if err != nil {
		return err
	}
	rt.ui.Section("Sibling instances")
	rt.ui.KeyValue("Parent", discovery.Parent)
	if len(discovery.Instances) == 0 {
		rt.ui.Info("No managed instances found in immediate child directories")
	}
	for _, instance := range discovery.Instances {
		marker := ""
		if instance.Root == rt.paths.Root {
			marker = " (current)"
		}
		rt.ui.KeyValue(instance.Config.Name+marker, instance.Root)
		rt.ui.Info("Image %s; web %s:%d; API 127.0.0.1:%d", instance.Config.EffectiveImage(), instance.Config.BindAddress, instance.Config.WebPort, instance.Config.APIPort)
	}
	for _, invalid := range discovery.Invalid {
		rt.ui.Warn("Invalid managed instance at %s: %s", invalid.Root, invalid.Reason)
	}
	return nil
}

func (rt runtime) menuUpdateCheck(ctx context.Context) error {
	result, err := rt.manager.CheckImageUpdate(ctx)
	if err != nil {
		return err
	}
	rt.ui.Section("Image update check")
	rt.ui.KeyValue("Tracked image", result.TrackedImage)
	if result.CurrentDigest != "" {
		rt.ui.KeyValue("Current digest", result.CurrentDigest)
		rt.ui.KeyValue("Current source", result.CurrentSource)
	}
	if result.RemoteDigest != "" {
		rt.ui.KeyValue("Remote digest", result.RemoteDigest)
	}
	if result.UpdateAvailable {
		rt.ui.Warn("A newer registry digest is available")
	} else {
		rt.ui.Success("Configured image matches the registry digest")
	}
	return nil
}

func (rt runtime) menuSchedule(ctx context.Context) error {
	choice, err := rt.ui.Menu("Scheduled backups", []ui.Item{
		{Key: "1", Label: "Show schedule"},
		{Key: "2", Label: "Install or replace schedule"},
		{Key: "3", Label: "Remove schedule"},
		{Key: "4", Label: "Run schedule now"},
		{Key: "0", Label: "Back"},
	})
	if err != nil {
		return err
	}
	switch choice {
	case "1":
		schedule, found, showErr := rt.manager.ShowBackupSchedule(ctx)
		if showErr != nil {
			return showErr
		}
		if !found {
			rt.ui.Info("No backup schedule is configured for this instance")
			return nil
		}
		rt.ui.Section("Backup schedule")
		rt.ui.KeyValue("Cron", schedule.Cron)
		rt.ui.KeyValue("Retention", fmt.Sprintf("%d scheduled backups", schedule.Keep))
		rt.ui.KeyValue("Executable", schedule.Executable)
		rt.ui.KeyValue("Instance", schedule.InstanceRoot)
		return nil
	case "2":
		mode, modeErr := rt.ui.Menu("Schedule type", []ui.Item{
			{Key: "1", Label: "Daily local time"},
			{Key: "2", Label: "Custom five-field cron"},
			{Key: "0", Label: "Cancel"},
		})
		if modeErr != nil {
			return modeErr
		}
		var cronValue string
		switch mode {
		case "1":
			daily, dailyErr := rt.ui.Prompt("Daily time (HH:MM)", "03:30")
			if dailyErr != nil {
				return dailyErr
			}
			parsed, parseErr := time.Parse("15:04", daily)
			if parseErr != nil || parsed.Format("15:04") != daily {
				return fmt.Errorf("daily time must use 24-hour HH:MM format")
			}
			cronValue = fmt.Sprintf("%d %d * * *", parsed.Minute(), parsed.Hour())
		case "2":
			cron, cronErr := rt.ui.Prompt("Cron expression", "30 3 * * *")
			if cronErr != nil {
				return cronErr
			}
			cronValue = strings.TrimSpace(cron)
		default:
			return nil
		}
		keep, keepErr := rt.ui.PromptInt("Scheduled backups to keep", 7)
		if keepErr != nil {
			return keepErr
		}
		executable, execErr := os.Executable()
		if execErr != nil {
			return fmt.Errorf("locate Rakazo Manager executable: %w", execErr)
		}
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		schedule, setErr := rt.manager.SetBackupSchedule(ctx, manager.BackupSchedule{
			Cron:       cronValue,
			Keep:       keep,
			Executable: executable,
		})
		if setErr != nil {
			return setErr
		}
		rt.ui.Success("Backup schedule installed")
		rt.ui.KeyValue("Cron", schedule.Cron)
		rt.ui.KeyValue("Retention", fmt.Sprintf("%d scheduled backups", schedule.Keep))
		return nil
	case "3":
		confirmed, confErr := rt.ui.Confirm("Remove the managed backup schedule?", false)
		if confErr != nil {
			return confErr
		}
		if !confirmed {
			return nil
		}
		removed, removeErr := rt.manager.RemoveBackupSchedule(ctx)
		if removeErr != nil {
			return removeErr
		}
		if removed {
			rt.ui.Success("Backup schedule removed")
		} else {
			rt.ui.Info("No backup schedule was configured")
		}
		return nil
	case "4":
		keep, keepErr := rt.ui.PromptInt("Scheduled backups to keep after this run", 7)
		if keepErr != nil {
			return keepErr
		}
		result, runErr := rt.manager.RunScheduledBackup(ctx, keep)
		if runErr != nil {
			return runErr
		}
		rt.ui.Success("Scheduled backup completed: %s", result.Archive)
		return nil
	default:
		return nil
	}
}

func (rt runtime) menuImportInstance(ctx context.Context) error {
	archive, err := rt.ui.Prompt("Path to instance export ZIP", "")
	if err != nil {
		return err
	}
	target, err := rt.ui.Prompt("Target folder (new or empty)", filepath.Join(filepath.Dir(rt.paths.Root), "recovered"))
	if err != nil {
		return err
	}
	name, err := rt.ui.Prompt("Instance name override (optional)", "")
	if err != nil {
		return err
	}
	startNow, err := rt.ui.Confirm("Start Rakazo and import the embedded backup now?", true)
	if err != nil {
		return err
	}
	options := manager.ImportInstanceOptions{
		Pull:  true,
		Start: startNow,
	}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		options.Patch.Name = &trimmed
	}
	targetRuntime, err := rt.app.runtime(target, rt.ui)
	if err != nil {
		return err
	}
	dryRun, err := rt.ui.Confirm("Dry-run first?", true)
	if err != nil {
		return err
	}
	if dryRun {
		options.DryRun = true
		result, previewErr := targetRuntime.manager.ImportInstance(ctx, archive, options)
		if previewErr != nil {
			return previewErr
		}
		rt.ui.Success("Import dry run completed for %s", target)
		rt.ui.KeyValue("Embedded backup", result.Plan.EmbeddedBackup)
		for _, action := range result.Plan.Actions {
			rt.ui.Info("%s", action)
		}
		applyNow, confErr := rt.ui.Confirm("Apply the import now?", false)
		if confErr != nil {
			return confErr
		}
		if !applyNow {
			return nil
		}
		options.DryRun = false
	}
	result, err := targetRuntime.manager.ImportInstance(ctx, archive, options)
	if err != nil {
		return err
	}
	rt.ui.Success("Instance import completed")
	rt.ui.KeyValue("Target", result.Plan.Target)
	if result.Plan.EmbeddedBackup != "" {
		rt.ui.KeyValue("Embedded backup", result.Plan.EmbeddedBackup)
	}
	return nil
}

func (rt runtime) menuDecommission(ctx context.Context) error {
	cfg, err := rt.manager.ShowConfig()
	if err != nil {
		return err
	}
	rt.ui.Section("Decommission")
	rt.ui.Warn("Default mode removes containers and generated manager files only.")
	rt.ui.Info("data/, workspace/, backups/, and docker-compose.override.yml are retained unless explicitly selected.")
	deleteData, err := rt.ui.Confirm("Also delete data/ (critical application state)?", false)
	if err != nil {
		return err
	}
	deleteWorkspace, err := rt.ui.Confirm("Also delete workspace/?", false)
	if err != nil {
		return err
	}
	deleteBackups, err := rt.ui.Confirm("Also delete backups/?", false)
	if err != nil {
		return err
	}
	deleteOverride, err := rt.ui.Confirm("Also delete docker-compose.override.yml?", false)
	if err != nil {
		return err
	}
	durableDeletion := deleteData || deleteWorkspace || deleteBackups || deleteOverride
	dryRun, err := rt.ui.Confirm("Dry-run first?", true)
	if err != nil {
		return err
	}
	allowDeletion := !durableDeletion || dryRun
	if durableDeletion && !dryRun {
		if err := rt.ui.RequirePhrase(
			fmt.Sprintf("Type the exact instance name to authorize durable deletion (%s).", cfg.Name),
			cfg.Name,
		); err != nil {
			return err
		}
		allowDeletion = true
	}
	options := manager.DecommissionOptions{
		DeleteData:        deleteData,
		DeleteWorkspace:   deleteWorkspace,
		DeleteBackups:     deleteBackups,
		DeleteOverride:    deleteOverride,
		AllowDataDeletion: allowDeletion,
		DryRun:            dryRun,
	}
	plan, err := rt.manager.PlanDecommission(options)
	if err != nil {
		return err
	}
	rt.ui.Section("Decommission plan")
	rt.ui.KeyValue("Instance", cfg.Name)
	rt.ui.Info("Would remove Rakazo APIs without Docker volumes")
	if plan.RemoveBackupSchedule {
		rt.ui.Info("Would remove any manager-owned backup schedule")
	}
	for _, path := range plan.Remove {
		rt.ui.Info("Would remove %s", path)
	}
	for _, path := range plan.Retain {
		rt.ui.Info("Would retain %s", path)
	}
	if dryRun {
		rt.ui.Success("Dry run complete; nothing was removed")
		applyNow, confErr := rt.ui.Confirm("Apply decommission now?", false)
		if confErr != nil {
			return confErr
		}
		if !applyNow {
			return nil
		}
		if durableDeletion {
			if err := rt.ui.RequirePhrase(
				fmt.Sprintf("Type the exact instance name to authorize durable deletion (%s).", cfg.Name),
				cfg.Name,
			); err != nil {
				return err
			}
			options.AllowDataDeletion = true
		}
		options.DryRun = false
	} else {
		confirmed, confErr := rt.ui.Confirm("Remove the instance containers and listed generated files now?", false)
		if confErr != nil {
			return confErr
		}
		if !confirmed {
			return fmt.Errorf("decommission cancelled")
		}
	}
	if _, err := rt.manager.Decommission(ctx, options); err != nil {
		return err
	}
	rt.ui.Success("Instance decommissioned")
	if !durableDeletion {
		rt.ui.Info("Persistent data/, workspace/, backups/, and the Compose override were retained")
	}
	return nil
}

func (rt runtime) printStorageLayout() {
	rt.ui.Section("Storage layout")
	rt.ui.KeyValue("Instance root", rt.paths.Root)
	rt.ui.KeyValue("./pg", rt.paths.Postgres)
	rt.ui.Info("Postgres files. Survives image updates.")
	rt.ui.KeyValue("./data", rt.paths.Data)
	rt.ui.Info("Bot homes, browser profiles, artifacts.")
	rt.ui.KeyValue("./workspace", rt.paths.Workspace)
	rt.ui.Info("Optional host project files.")
	rt.ui.KeyValue("./backups", rt.paths.Backups)
	rt.ui.Section("What updates preserve")
	rt.ui.Info("Update = backup → pull GHCR image → recreate app containers → verify mounts and /health.")
	rt.ui.Info("./pg, ./data, and ./backups are bind mounts and are never removed by update.")
}

func (rt runtime) printInstallSummary(cfg config.Config) {
	rt.ui.KeyValue("Folder", rt.paths.Root)
	rt.ui.KeyValue("Instance", cfg.Name)
	rt.ui.KeyValue("Web", cfg.PublicOrigin())
	rt.ui.KeyValue("Listening on", cfg.BindAddress)
	if cfg.Origin != "" {
		rt.ui.Info("Point host Caddy at 127.0.0.1:%d and use the origin above for TLS.", cfg.WebPort)
	}
	rt.ui.KeyValue("API port", cfg.APIPort)
	rt.ui.KeyValue("Postgres port", cfg.PostgresPort)
	if cfg.RebuildComposeOnStart {
		rt.ui.KeyValue("Compose on start", "rebuild full file")
	} else {
		rt.ui.KeyValue("Compose on start", "preserve custom edits")
	}
	rt.ui.KeyValue("./pg", rt.paths.Postgres)
	rt.ui.KeyValue("./data", rt.paths.Data)
	rt.ui.Info("The first registered user becomes the deployment owner.")
	rt.ui.Info("Force a one-shot compose rebuild with: rakazo-manager start --rebuild %q", rt.paths.Root)
}

func (rt runtime) promptInstallPorts(bindAddress string) (webPort, apiPort, postgresPort int, err error) {
	suggestedWeb, suggestedAPI, suggestedPostgres, err := ports.Suggest(rt.paths.Root, bindAddress)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("detect free ports: %w", err)
	}
	if reserved := ports.ReservedBySiblings(rt.paths.Root); len(reserved) > 0 {
		rt.ui.Info("Detected %d port(s) reserved by other Rakazo instances under %s", len(reserved), filepath.Dir(rt.paths.Root))
	}
	rt.ui.Section("Host ports")
	rt.ui.Info("Defaults avoid ports already used on this host and ports reserved by sibling Rakazo installs.")
	rt.ui.KeyValue("Suggested web", suggestedWeb)
	rt.ui.KeyValue("Suggested API", suggestedAPI)
	rt.ui.KeyValue("Suggested Postgres", suggestedPostgres)

	useAutomatic, err := rt.ui.Confirm("Use these free ports automatically?", true)
	if err != nil {
		return 0, 0, 0, err
	}
	if useAutomatic {
		return 0, 0, 0, nil
	}

	for {
		webPort, err = rt.ui.PromptInt("Web host port", suggestedWeb)
		if err != nil {
			return 0, 0, 0, err
		}
		if err := ports.Check(rt.paths.Root, bindAddress, webPort, nil); err != nil {
			rt.ui.Failure("%v", err)
			continue
		}
		break
	}
	for {
		apiPort, err = rt.ui.PromptInt("API host port", suggestedAPI)
		if err != nil {
			return 0, 0, 0, err
		}
		if err := ports.Check(rt.paths.Root, bindAddress, apiPort, map[int]bool{webPort: true}); err != nil {
			rt.ui.Failure("%v", err)
			continue
		}
		break
	}
	for {
		postgresPort, err = rt.ui.PromptInt("Postgres host port", suggestedPostgres)
		if err != nil {
			return 0, 0, 0, err
		}
		if err := ports.Check(rt.paths.Root, bindAddress, postgresPort, map[int]bool{webPort: true, apiPort: true}); err != nil {
			rt.ui.Failure("%v", err)
			continue
		}
		break
	}
	return webPort, apiPort, postgresPort, nil
}
