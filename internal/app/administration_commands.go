package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/manager"
	"github.com/nicolaeser/RakazoManager/internal/stack"
	"github.com/nicolaeser/RakazoManager/internal/ui"
)

func (app *App) instancesCommand(args []string, terminal *ui.UI) error {
	if len(args) == 0 || args[0] != "list" {
		return fmt.Errorf("usage: rakazo-manager instances list [--json] [PARENT]")
	}
	flags := flag.NewFlagSet("instances list", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	parent, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager instances list [--json] [PARENT]")
	}
	if len(flags.Args()) == 0 {
		if paths, pathsErr := stack.NewPaths(parent); pathsErr == nil {
			if _, statErr := os.Stat(paths.Config); statErr == nil {
				parent = filepath.Dir(paths.Root)
			}
		}
	}
	discovery, err := manager.DiscoverInstances(parent)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSONEnvelope(app.Out, "instances-list", discovery)
	}
	terminal.Section("Rakazo Manager instances")
	terminal.KeyValue("Parent", discovery.Parent)
	if len(discovery.Instances) == 0 {
		terminal.Info("No managed instances found in immediate child directories")
	}
	for _, instance := range discovery.Instances {
		terminal.KeyValue(instance.Config.Name, instance.Root)
		terminal.Info("Image %s; web %s:%d; API 127.0.0.1:%d", instance.Config.EffectiveImage(), instance.Config.BindAddress, instance.Config.WebPort, instance.Config.APIPort)
	}
	for _, invalid := range discovery.Invalid {
		terminal.Warn("Invalid managed instance at %s: %s", invalid.Root, invalid.Reason)
	}
	return nil
}

func (app *App) scheduleCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rakazo-manager schedule set|show|remove|run [OPTIONS] [FOLDER]")
	}
	switch args[0] {
	case "set":
		return app.scheduleSetCommand(ctx, terminal, args[1:])
	case "show":
		return app.scheduleShowCommand(ctx, terminal, args[1:])
	case "remove":
		return app.scheduleRemoveCommand(ctx, terminal, args[1:])
	case "run":
		return app.scheduleRunCommand(ctx, terminal, args[1:])
	default:
		return fmt.Errorf("unknown schedule action %q; choose set, show, remove, or run", args[0])
	}
}

func (app *App) scheduleSetCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("schedule set", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	daily := flags.String("daily", "03:00", "daily local time in 24-hour HH:MM format")
	cronExpression := flags.String("cron", "", "advanced five-field cron expression instead of --daily")
	keep := flags.Int("keep", 7, "number of scheduled backups to retain")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager schedule set [--daily HH:MM | --cron EXPR] [--keep N] [FOLDER]")
	}
	cronValue := strings.TrimSpace(*cronExpression)
	if cronValue == "" {
		parsed, parseErr := time.Parse("15:04", *daily)
		if parseErr != nil || parsed.Format("15:04") != *daily {
			return fmt.Errorf("--daily must use 24-hour HH:MM format")
		}
		cronValue = fmt.Sprintf("%d %d * * *", parsed.Minute(), parsed.Hour())
	} else {
		visitedDaily := false
		flags.Visit(func(item *flag.Flag) {
			if item.Name == "daily" {
				visitedDaily = true
			}
		})
		if visitedDaily {
			return fmt.Errorf("--daily and --cron are mutually exclusive")
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate Rakazo Manager executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return fmt.Errorf("resolve Rakazo Manager executable: %w", err)
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	schedule, err := rt.manager.SetBackupSchedule(ctx, manager.BackupSchedule{
		Cron:       cronValue,
		Keep:       *keep,
		Executable: executable,
	})
	if err != nil {
		return err
	}
	terminal.Success("Scheduled backup installed")
	terminal.KeyValue("Cron", schedule.Cron)
	terminal.KeyValue("Retention", fmt.Sprintf("%d scheduled backups", schedule.Keep))
	terminal.KeyValue("Executable", schedule.Executable)
	terminal.KeyValue("Instance", schedule.InstanceRoot)
	terminal.Info("The cron expression uses the host's local timezone.")
	return nil
}

func (app *App) scheduleShowCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("schedule show", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager schedule show [--json] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	schedule, found, err := rt.manager.ShowBackupSchedule(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSONEnvelope(app.Out, "schedule-show", struct {
			Configured bool                   `json:"configured"`
			Schedule   manager.BackupSchedule `json:"schedule"`
		}{Configured: found, Schedule: schedule})
	}
	terminal.Section("Scheduled backups")
	if !found {
		terminal.Info("No backup schedule is configured for this instance")
		return nil
	}
	terminal.KeyValue("Cron", schedule.Cron)
	terminal.KeyValue("Retention", fmt.Sprintf("%d scheduled backups", schedule.Keep))
	terminal.KeyValue("Executable", schedule.Executable)
	terminal.KeyValue("Instance", schedule.InstanceRoot)
	return nil
}

func (app *App) scheduleRemoveCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	root, err := oneFolder(args)
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager schedule remove [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	removed, err := rt.manager.RemoveBackupSchedule(ctx)
	if err != nil {
		return err
	}
	if !removed {
		terminal.Info("No backup schedule was configured")
		return nil
	}
	terminal.Success("Scheduled backup removed; unrelated crontab entries were preserved")
	return nil
}

func (app *App) scheduleRunCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("schedule run", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	keep := flags.Int("keep", 7, "number of scheduled backups to retain")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager schedule run [--keep N] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	result, err := rt.manager.RunScheduledBackup(ctx, *keep)
	if err != nil {
		return err
	}
	terminal.Success("Scheduled backup created and verified: %s", result.Archive)
	if len(result.Pruned) > 0 {
		terminal.Info("Pruned %d older scheduled backup(s)", len(result.Pruned))
	}
	return nil
}

func (app *App) configCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: rakazo-manager config show|set [OPTIONS] [FOLDER]")
	}
	switch args[0] {
	case "show":
		return app.configShowCommand(terminal, args[1:])
	case "set":
		return app.configSetCommand(ctx, terminal, args[1:])
	default:
		return fmt.Errorf("unknown config action %q; choose show or set", args[0])
	}
}

func (app *App) configShowCommand(terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("config show", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager config show [--json] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	cfg, err := rt.manager.ShowConfig()
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeJSONEnvelope(app.Out, "config-show", cfg)
	}
	terminal.Section("Managed instance configuration")
	terminal.KeyValue("Folder", rt.paths.Root)
	terminal.KeyValue("Name", cfg.Name)
	terminal.KeyValue("Tracked image", cfg.Image)
	if cfg.PinnedImage != "" {
		terminal.KeyValue("Rollback pin", cfg.PinnedImage)
	}
	terminal.KeyValue("Effective image", cfg.EffectiveImage())
	terminal.KeyValue("Bind address", cfg.BindAddress)
	terminal.KeyValue("Origin", cfg.PublicOrigin())
	terminal.KeyValue("Web port", cfg.WebPort)
	terminal.KeyValue("API port", cfg.APIPort)
	terminal.KeyValue("Postgres port", cfg.PostgresPort)
	terminal.KeyValue("Rebuild on start", cfg.RebuildComposeOnStart)
	return nil
}

func (app *App) configSetCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("config set", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	name := flags.String("name", "", "set the managed container/project name")
	image := flags.String("image", "", "set the tracked Rakazo image and clear any rollback pin")
	webPort := flags.Int("web-port", 0, "set the web host port")
	apiPort := flags.Int("api-port", 0, "set the API host port")
	postgresPort := flags.Int("postgres-port", 0, "set the Postgres host port")
	bindAddress := flags.String("bind-address", "", "set 127.0.0.1 or 0.0.0.0")
	origin := flags.String("origin", "", "set the public origin (IP, domain, or URL); empty restores localhost")
	rebuildOnStart := flags.Bool("rebuild-on-start", false, "set whether start regenerates the base Compose file (use =false to disable)")
	dryRun := flags.Bool("dry-run", false, "validate and print changes without changing files or containers")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager config set [OPTIONS] [FOLDER]")
	}
	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	patch := manager.ConfigPatch{}
	if visited["name"] {
		patch.Name = name
	}
	if visited["image"] {
		patch.Image = image
	}
	if visited["web-port"] {
		patch.WebPort = webPort
	}
	if visited["api-port"] {
		patch.APIPort = apiPort
	}
	if visited["postgres-port"] {
		patch.PostgresPort = postgresPort
	}
	if visited["bind-address"] {
		patch.BindAddress = bindAddress
	}
	if visited["origin"] {
		patch.Origin = origin
	}
	if visited["rebuild-on-start"] {
		patch.RebuildComposeOnStart = rebuildOnStart
	}
	if patch == (manager.ConfigPatch{}) {
		return fmt.Errorf("config set requires at least one configuration option")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}

	plan, err := rt.manager.ApplyConfigPatch(ctx, patch, true)
	if err != nil {
		return err
	}
	if len(plan.Changes) == 0 {
		terminal.Success("Configuration already matches the requested values")
		return nil
	}
	publicBindChange := plan.Before.BindAddress != config.PublicBindAddress && plan.After.BindAddress == config.PublicBindAddress
	if publicBindChange && *dryRun {
		terminal.Warn("The planned web UI would listen on every host interface (0.0.0.0). API and Postgres stay on 127.0.0.1. Protect the web UI with a firewall, VPN, or reverse proxy.")
	}
	if publicBindChange && !*dryRun {
		if err := terminal.RequirePhrase("Publishing on 0.0.0.0 exposes the web UI on every interface. API and Postgres stay on 127.0.0.1. Protect the web UI with a firewall, VPN, or reverse proxy.", "BIND-ALL"); err != nil {
			return err
		}
	}
	if !*dryRun {
		plan, err = rt.manager.ApplyConfigPatch(ctx, patch, false)
		if err != nil {
			return err
		}
	}
	title := "Configuration changes"
	if *dryRun {
		title = "Configuration dry run — no changes made"
	}
	terminal.Section(title)
	sort.SliceStable(plan.Changes, func(left, right int) bool { return plan.Changes[left].Field < plan.Changes[right].Field })
	for _, change := range plan.Changes {
		terminal.KeyValue(change.Field, fmt.Sprintf("%v -> %v", change.Before, change.After))
	}
	if plan.RuntimeAction != "" {
		terminal.KeyValue("Runtime action", configRuntimeActionDescription(plan.RuntimeAction))
	}
	if *dryRun && plan.RequiresComposeUpdate {
		composePreview, previewErr := rt.manager.PreviewManagedCompose(plan.After)
		if previewErr != nil {
			return previewErr
		}
		terminal.Section("Planned managed Compose state")
		fmt.Fprint(app.Out, composePreview)
	}
	if *dryRun {
		return nil
	}
	terminal.Success("Configuration saved; %s", configRuntimeResultDescription(plan.RuntimeAction))
	return nil
}

func configRuntimeActionDescription(action string) string {
	switch action {
	case "metadata-only":
		return "update instance metadata only"
	case "update-compose-only":
		return "update and validate Compose; no service exists to recreate"
	case "recreate-stopped-service":
		return "update Compose and recreate the stopped service"
	case "recreate-running-service-and-verify":
		return "update Compose, recreate the running service, and verify mounts and health"
	default:
		return action
	}
}

func configRuntimeResultDescription(action string) string {
	switch action {
	case "metadata-only":
		return "no Compose or container change was required"
	case "update-compose-only":
		return "Compose was validated and no container existed to recreate"
	case "recreate-stopped-service":
		return "the stopped service was recreated and remains stopped"
	case "recreate-running-service-and-verify":
		return "the running service was recreated and its mounts and health were verified"
	default:
		return "the requested runtime action completed"
	}
}

func boolWord(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func (app *App) decommissionCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("decommission", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	deleteData := flags.Bool("delete-data", false, "also permanently delete data/ (requires an exact instance-name guard)")
	deleteWorkspace := flags.Bool("delete-workspace", false, "also permanently delete workspace/ (requires an exact instance-name guard)")
	deleteBackups := flags.Bool("delete-backups", false, "also permanently delete backups/ (requires an exact instance-name guard)")
	deleteOverride := flags.Bool("delete-override", false, "also delete the user-owned Compose override (requires an exact instance-name guard)")
	confirmation := flags.String("confirm-delete-data", "", "exact instance name required for any durable deletion")
	dryRun := flags.Bool("dry-run", false, "print exact container, file, and retention actions without changing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager decommission [OPTIONS] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	cfg, err := rt.manager.ShowConfig()
	if err != nil {
		return err
	}
	durableDeletion := *deleteData || *deleteWorkspace || *deleteBackups || *deleteOverride
	allowDeletion := !durableDeletion || *dryRun
	if durableDeletion {
		terminal.Warn("The selected paths contain persistent data or user-owned configuration and cannot be recovered by Rakazo Manager.")
	}
	if durableDeletion && !*dryRun {
		token := *confirmation
		if token == "" {
			token, err = terminal.Prompt("Type the exact instance name "+cfg.Name+" to authorize deletion", "")
			if err != nil {
				return err
			}
		}
		if token != cfg.Name {
			return fmt.Errorf("--confirm-delete-data must exactly match instance name %q", cfg.Name)
		}
		allowDeletion = true
	}
	options := manager.DecommissionOptions{
		DeleteData:        *deleteData,
		DeleteWorkspace:   *deleteWorkspace,
		DeleteBackups:     *deleteBackups,
		DeleteOverride:    *deleteOverride,
		AllowDataDeletion: allowDeletion,
		DryRun:            *dryRun,
	}
	plan, err := rt.manager.PlanDecommission(options)
	if err != nil {
		return err
	}
	title := "Decommission plan"
	if *dryRun {
		title = "Decommission dry run — no changes made"
	}
	terminal.Section(title)
	terminal.KeyValue("Instance", cfg.Name)
	terminal.KeyValue("Folder", plan.Root)
	terminal.Info("Would remove Rakazo APIs without Docker volumes")
	if plan.RemoveBackupSchedule {
		terminal.Info("Would remove any manager-owned backup schedule")
	}
	for _, target := range plan.Remove {
		terminal.Info("Would remove %s", target)
	}
	for _, target := range plan.Retain {
		terminal.Info("Would retain %s", target)
	}
	if *dryRun {
		return nil
	}
	confirmed, err := terminal.Confirm("Remove the instance containers and listed generated files now?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("decommission cancelled")
	}
	_, err = rt.manager.Decommission(ctx, options)
	if err != nil {
		return err
	}
	terminal.Success("Instance decommissioned")
	if !durableDeletion {
		terminal.Info("Persistent data/, workspace/, backups/, and the Compose override were retained")
	}
	return nil
}

func (app *App) importInstanceCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("import-instance", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	name := flags.String("name", "", "override the archived managed container/project name")
	image := flags.String("image", "", "override the archived tracked Rakazo image")
	webPort := flags.Int("web-port", 0, "override the archived web host port")
	apiPort := flags.Int("api-port", 0, "override the archived API host port")
	postgresPort := flags.Int("postgres-port", 0, "override the archived Postgres host port")
	bindAddress := flags.String("bind-address", "", "override the archived bind address with 127.0.0.1 or 0.0.0.0")
	origin := flags.String("origin", "", "override the archived public origin")
	noPull := flags.Bool("no-pull", false, "do not pull the recovered image")
	noStart := flags.Bool("no-start", false, "stage the recovered instance and embedded backup without importing data yet")
	dryRun := flags.Bool("dry-run", false, "validate the export, target, ports, and actions without changing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	if len(remaining) < 1 || len(remaining) > 2 {
		return fmt.Errorf("usage: rakazo-manager import-instance [OPTIONS] EXPORT.zip [FOLDER]")
	}
	root := "."
	if len(remaining) == 2 {
		root = remaining[1]
	}
	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	patch := manager.ConfigPatch{}
	if visited["name"] {
		patch.Name = name
	}
	if visited["image"] {
		patch.Image = image
	}
	if visited["web-port"] {
		patch.WebPort = webPort
	}
	if visited["api-port"] {
		patch.APIPort = apiPort
	}
	if visited["postgres-port"] {
		patch.PostgresPort = postgresPort
	}
	if visited["bind-address"] {
		patch.BindAddress = bindAddress
	}
	if visited["origin"] {
		patch.Origin = origin
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	options := manager.ImportInstanceOptions{
		Pull:   !*noPull,
		Start:  !*noStart,
		DryRun: *dryRun,
		Patch:  patch,
	}
	plan, err := rt.manager.PlanImportInstance(remaining[0], options)
	if err != nil {
		return err
	}
	title := "Instance recovery plan"
	if *dryRun {
		title = "Import dry run — no changes made"
	}
	terminal.Section(title)
	terminal.KeyValue("Archive", plan.Archive)
	terminal.KeyValue("Target", plan.Target)
	terminal.KeyValue("Instance", plan.Config.Name)
	terminal.KeyValue("Image", plan.Config.EffectiveImage())
	terminal.KeyValue("Bind", plan.Config.BindAddress)
	terminal.KeyValue("Origin", plan.Config.PublicOrigin())
	terminal.KeyValue("Web port", plan.Config.WebPort)
	terminal.KeyValue("API port", plan.Config.APIPort)
	terminal.KeyValue("Postgres port", plan.Config.PostgresPort)
	terminal.KeyValue("Workspace included", boolWord(plan.IncludesWorkspace))
	terminal.KeyValue("Embedded backup", plan.EmbeddedBackup)
	for _, action := range plan.Actions {
		terminal.Info("Would %s", action)
	}
	terminal.Warn("The instance export contains secrets.env and must be handled as sensitive data.")
	if plan.Config.BindAddress == config.PublicBindAddress && *dryRun {
		terminal.Warn("The planned web UI would listen on every host interface (0.0.0.0). API and Postgres stay on 127.0.0.1. Protect the web UI with a firewall, VPN, or reverse proxy.")
	}
	if *dryRun {
		composePreview, previewErr := rt.manager.PreviewManagedCompose(plan.Config)
		if previewErr != nil {
			return previewErr
		}
		terminal.Section("Planned docker-compose.yml")
		fmt.Fprint(app.Out, composePreview)
		return nil
	}
	if plan.Config.BindAddress == config.PublicBindAddress {
		if err := terminal.RequirePhrase("The recovered web UI will be published on every interface (0.0.0.0). API and Postgres stay on 127.0.0.1. Protect the web UI with a firewall, VPN, or reverse proxy.", "BIND-ALL"); err != nil {
			return err
		}
	}
	confirmed, err := terminal.Confirm("Recover this export into the empty target now?", false)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("instance import cancelled")
	}
	result, err := rt.manager.ImportInstance(ctx, remaining[0], options)
	if err != nil {
		return err
	}
	if result.Restored {
		terminal.Success("Instance recovered, started, imported, and verified")
		return nil
	}
	terminal.Success("Instance files and embedded backup were recovered without starting Rakazo")
	terminal.Info("Complete recovery with: rakazo-manager start %q", plan.Target)
	terminal.Info("Then run: rakazo-manager restore %q %q", filepath.Base(plan.EmbeddedBackup), plan.Target)
	return nil
}
