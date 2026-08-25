package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/manager"
	"github.com/nicolaeser/RakazoManager/internal/stack"
	"github.com/nicolaeser/RakazoManager/internal/ui"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

type App struct {
	Build  BuildInfo
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	Runner command.Runner
}

type runtime struct {
	app     *App
	paths   stack.Paths
	manager *manager.Manager
	ui      *ui.UI
}

func New(build BuildInfo) *App {
	return &App{
		Build:  build,
		In:     os.Stdin,
		Out:    os.Stdout,
		Err:    os.Stderr,
		Runner: command.OSRunner{},
	}
}

func (app *App) Run(ctx context.Context, args []string) error {
	global := flag.NewFlagSet("rakazo-manager", flag.ContinueOnError)
	global.SetOutput(app.Err)
	assumeYes := global.Bool("yes", false, "confirm guarded operations")
	noColor := global.Bool("no-color", false, "disable ANSI styling")
	showHelp := false
	global.BoolVar(&showHelp, "help", false, "show help")
	global.BoolVar(&showHelp, "h", false, "show help")
	if err := global.Parse(args); err != nil {
		return err
	}
	if showHelp {
		app.printHelp()
		return nil
	}
	remaining := global.Args()
	commandName := "menu"
	if len(remaining) > 0 {
		commandName = remaining[0]
		remaining = remaining[1:]
	}

	color := false
	if output, ok := app.Out.(*os.File); ok {
		color = ui.ColorEnabled(output, *noColor)
	}
	terminal := ui.New(app.In, app.Out, app.Err, color, *assumeYes)
	switch commandName {
	case "menu", "help", "--help", "-h", "version", "completion", "man", "shell":
		return app.dispatch(ctx, terminal, commandName, remaining)
	default:
		cmdCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		err := app.dispatch(cmdCtx, terminal, commandName, remaining)
		if command.IsInterrupted(err) {
			return nil
		}
		return err
	}
}

func (app *App) runtime(root string, terminal *ui.UI) (runtime, error) {
	paths, err := stack.NewPaths(root)
	if err != nil {
		return runtime{}, err
	}
	mgr := manager.New(paths, app.Runner, app.In, app.Out, app.Err)
	mgr.Progress = terminal.Step
	return runtime{
		app:     app,
		paths:   paths,
		manager: mgr,
		ui:      terminal,
	}, nil
}

func (app *App) dispatch(ctx context.Context, terminal *ui.UI, commandName string, args []string) error {
	switch commandName {
	case "help", "--help", "-h":
		app.printHelp()
		return nil
	case "version":
		return app.versionCommand(args)
	case "completion":
		return app.completionCommand(args)
	case "man":
		return app.manCommand(args)
	case "self-update":
		return app.selfUpdateCommand(ctx, terminal, args)
	case "install":
		return app.installCommand(ctx, terminal, args)
	case "instances":
		return app.instancesCommand(args, terminal)
	case "config":
		return app.configCommand(ctx, terminal, args)
	case "schedule":
		return app.scheduleCommand(ctx, terminal, args)
	case "decommission":
		return app.decommissionCommand(ctx, terminal, args)
	case "import-instance":
		return app.importInstanceCommand(ctx, terminal, args)
	case "menu":
		root, err := oneFolder(args)
		if err != nil {
			return fmt.Errorf("usage: rakazo-manager menu [FOLDER]")
		}
		rt, err := app.runtime(root, terminal)
		if err != nil {
			return err
		}
		return rt.mainMenu(ctx)
	case "start":
		return app.startCommand(ctx, terminal, args)
	case "status":
		return app.statusCLICommand(ctx, terminal, args)
	case "update":
		return app.updateCommand(ctx, terminal, args)
	case "stop", "restart", "rollback":
		root, err := oneFolder(args)
		if err != nil {
			return fmt.Errorf("usage: rakazo-manager %s [FOLDER]", commandName)
		}
		rt, err := app.runtime(root, terminal)
		if err != nil {
			return err
		}
		return rt.simpleCommand(ctx, commandName)
	case "dashboard", "open":
		return app.dashboardCLICommand(ctx, terminal, args)
	case "backups":
		return app.backupsCommand(ctx, terminal, args)
	case "doctor":
		return app.doctorCommand(ctx, terminal, args)
	case "export-instance":
		return app.exportInstanceCommand(ctx, terminal, args)
	case "logs":
		return app.logsCommand(ctx, terminal, args)
	case "shell":
		return app.shellCommand(ctx, terminal, args)
	case "backup":
		return app.backupCommand(ctx, terminal, args)
	case "restore":
		return app.restoreCommand(ctx, terminal, args)
	default:
		return fmt.Errorf("unknown command %q; run rakazo-manager help", commandName)
	}
}

func (app *App) installCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("install", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	name := flags.String("name", "", "container/project name (default: derived from folder)")
	image := flags.String("image", "", "Rakazo GHCR image (default: "+config.DefaultImage+")")
	webPort := flags.Int("web-port", 0, "web host port (default: automatic)")
	apiPort := flags.Int("api-port", 0, "API host port (default: automatic)")
	postgresPort := flags.Int("postgres-port", 0, "Postgres host port (default: automatic)")
	bindAll := flags.Bool("bind-all", false, "publish the web UI on 0.0.0.0; API and Postgres stay on 127.0.0.1")
	origin := flags.String("origin", "", "public origin: IP, domain, or URL (default: http://127.0.0.1:<web-port>)")
	noPull := flags.Bool("no-pull", false, "do not pull the image")
	noStart := flags.Bool("no-start", false, "do not start the stack")
	rebuildOnStart := flags.Bool("rebuild-on-start", false, "regenerate docker-compose.yml on every start")
	openRouterFile := flags.String("openrouter-key-file", "", "optional OpenRouter API key file ('-' reads standard input)")
	dryRun := flags.Bool("dry-run", false, "print the configuration, Compose file, and actions without changing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager install [OPTIONS] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	start := !*noStart
	options := manager.InstallOptions{
		Name:         *name,
		Image:        *image,
		WebPort:      *webPort,
		APIPort:      *apiPort,
		PostgresPort: *postgresPort,
		BindAll:      *bindAll,
		Origin:       *origin,
		Pull:         !*noPull,
		Start:        start,
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "rebuild-on-start" {
			options.HasRebuildComposeOnStart = true
			options.RebuildComposeOnStart = *rebuildOnStart
		}
	})
	if *openRouterFile != "" {
		key, err := app.readSecretFile(*openRouterFile)
		if err != nil {
			return err
		}
		options.OpenRouterAPIKey = key
	}
	if *dryRun {
		preview, previewErr := rt.manager.PreviewInstall(options)
		if previewErr != nil {
			return previewErr
		}
		terminal.Section("Install dry run — no changes made")
		terminal.KeyValue("Folder", rt.paths.Root)
		terminal.KeyValue("Mode", map[bool]string{true: "create", false: "repair"}[preview.Created])
		terminal.KeyValue("Instance", preview.Config.Name)
		terminal.KeyValue("Image", preview.Config.EffectiveImage())
		terminal.KeyValue("Bind", preview.Config.BindAddress)
		if preview.Config.BindAddress == config.PublicBindAddress {
			terminal.Warn("The planned web UI would listen on every host interface (0.0.0.0). API and Postgres stay on 127.0.0.1. Protect the web UI with a firewall, VPN, or reverse proxy.")
		}
		terminal.KeyValue("Origin", preview.Config.PublicOrigin())
		terminal.KeyValue("Web port", preview.Config.WebPort)
		terminal.KeyValue("API port", preview.Config.APIPort)
		terminal.KeyValue("Postgres port", preview.Config.PostgresPort)
		for _, action := range preview.Actions {
			terminal.Info("Would %s", action)
		}
		terminal.Section("Planned docker-compose.yml")
		fmt.Fprint(app.Out, preview.Compose)
		return nil
	}
	terminal.Info("Preparing Rakazo instance at %s", rt.paths.Root)
	terminal.Info("Postgres lives under ./pg. Bot homes and artifacts live under ./data.")
	if *webPort == 0 && *apiPort == 0 && *postgresPort == 0 {
		terminal.Info("Host ports are automatic: free ports are chosen, skipping live listeners and sibling Rakazo reservations.")
	}
	if *bindAll {
		terminal.Warn("The web UI will listen on every host interface (0.0.0.0). API and Postgres stay on 127.0.0.1.")
	}
	result, err := rt.manager.Install(ctx, options)
	if err != nil {
		return err
	}
	switch {
	case result.Created && start:
		terminal.Success("Rakazo installed and started")
	case result.Created:
		terminal.Success("Rakazo installed (not started)")
		terminal.Info("Adjust docker-compose.yml or migrate data if needed, then run: rakazo-manager start %q", rt.paths.Root)
	case start:
		terminal.Success("Rakazo installation repaired and started")
	default:
		terminal.Success("Rakazo installation repaired (not started)")
	}
	rt.printInstallSummary(result.Config)
	return nil
}

func (app *App) logsCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("logs", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	tail := flags.Int("tail", 100, "number of existing lines")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager logs [--tail N] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.manager.Logs(ctx, *tail)
}

func (app *App) shellCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	folderArgs := args
	commandArgs := []string(nil)
	for i, arg := range args {
		if arg == "--" {
			folderArgs = args[:i]
			commandArgs = args[i+1:]
			break
		}
	}
	root, err := oneFolder(folderArgs)
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager shell [FOLDER] [-- COMMAND...]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.manager.Shell(ctx, commandArgs...)
}

func (app *App) backupCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	label := flags.String("label", "manual", "short backup label")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager backup [--label LABEL] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	archive, err := rt.manager.Backup(ctx, *label)
	if err != nil {
		return err
	}
	terminal.Success("Backup created and verified: %s", archive)
	return nil
}

func (app *App) restoreCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	dryRun := flags.Bool("dry-run", false, "validate the archive and print restore actions without changing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	remaining := flags.Args()
	if len(remaining) < 1 || len(remaining) > 2 {
		return fmt.Errorf("usage: rakazo-manager restore [--dry-run] BACKUP.zip [FOLDER]")
	}
	root := "."
	if len(remaining) == 2 {
		root = remaining[1]
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	if *dryRun {
		preview, previewErr := rt.manager.PreviewRestore(remaining[0])
		if previewErr != nil {
			return previewErr
		}
		terminal.Section("Restore dry run — no changes made")
		terminal.KeyValue("Source", preview.Source)
		terminal.KeyValue("Staged path", preview.StagedPath)
		for _, action := range preview.Actions {
			terminal.Info("Would %s", action)
		}
		return nil
	}
	if err := terminal.RequirePhrase("Restore replaces Rakazo configuration and user data. A safety backup is created first.", "RESTORE"); err != nil {
		return err
	}
	if err := rt.manager.Restore(ctx, remaining[0]); err != nil {
		return err
	}
	terminal.Success("Restore completed")
	return nil
}

func oneFolder(args []string) (string, error) {
	switch len(args) {
	case 0:
		return ".", nil
	case 1:
		return args[0], nil
	default:
		return "", fmt.Errorf("too many folder arguments")
	}
}

func (app *App) startCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	rebuild := flags.Bool("rebuild", false, "regenerate docker-compose.yml from the managed template before start")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager start [--rebuild] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	return rt.withSuccess("Rakazo started", rt.manager.Start(ctx, manager.StartOptions{Rebuild: *rebuild}))
}

func (rt runtime) simpleCommand(ctx context.Context, commandName string) error {
	switch commandName {
	case "stop":
		return rt.withSuccess("Rakazo stopped; persistent data was retained", rt.manager.Stop(ctx))
	case "restart":
		return rt.withSuccess("Rakazo restarted", rt.manager.Restart(ctx))
	case "status":
		return rt.statusCommand(ctx)
	case "rollback":
		if err := rt.ui.RequirePhrase("Rollback recreates Rakazo with the previously recorded image.", "ROLLBACK"); err != nil {
			return err
		}
		return rt.withSuccess("Previous image restored", rt.manager.Rollback(ctx))
	default:
		return fmt.Errorf("unsupported command %q", commandName)
	}
}

func (rt runtime) statusCommand(ctx context.Context) error {
	status, err := rt.manager.Status(ctx)
	if err != nil {
		return err
	}
	rt.ui.Section("Rakazo instance")
	rt.ui.KeyValue("Folder", status.Root)
	rt.ui.KeyValue("Name", status.Name)
	rt.ui.KeyValue("Image", status.Image)
	if status.Image != status.TrackedImage {
		rt.ui.KeyValue("Update channel", status.TrackedImage)
	}
	rt.ui.KeyValue("Web", status.WebURL)
	rt.ui.KeyValue("Web bind", fmt.Sprintf("%s:%d", status.BindAddress, status.WebPort))
	rt.ui.KeyValue("API bind", fmt.Sprintf("%s:%d", config.DefaultBindAddress, status.APIPort))
	rt.ui.KeyValue("Postgres bind", fmt.Sprintf("%s:%d", config.DefaultBindAddress, status.PostgresPort))
	if status.RebuildComposeOnStart {
		rt.ui.KeyValue("Compose on start", "rebuild full docker-compose.yml")
	} else {
		rt.ui.KeyValue("Compose on start", "preserve custom edits (use start --rebuild to force)")
	}
	rt.ui.KeyValue("API health", fmt.Sprintf("http://127.0.0.1:%d/health", status.APIPort))
	rt.ui.KeyValue("Postgres data", status.PostgresData)
	rt.ui.KeyValue("Application data", status.AppData)
	rt.ui.KeyValue("Project workspace", status.Workspace)
	rt.ui.KeyValue("Backups", status.Backups)
	rt.ui.Info("Updates recreate application containers only; host ./pg, ./data, and ./backups are bind-mounted and kept.")
	if status.Version != "" {
		rt.ui.KeyValue("Revision", status.Version)
	}
	if status.APIInfo != "" {
		if status.APIHealthy {
			rt.ui.KeyValue("API health", status.APIInfo)
		} else {
			rt.ui.Warn("API health: %s", status.APIInfo)
		}
	}
	fmt.Fprintln(rt.app.Out, "\n"+status.Containers)
	return nil
}

func (rt runtime) dashboardCommand() error {
	access, err := rt.manager.Access()
	if err != nil {
		return err
	}
	rt.ui.Section("Rakazo access")
	rt.ui.KeyValue("Web", access.URL)
	rt.ui.KeyValue("API health", access.API)
	rt.ui.KeyValue("Listening on", access.Listens)
	rt.ui.Info("The first registered user becomes the deployment owner.")
	if strings.HasPrefix(access.Listens, "0.0.0.0:") {
		rt.ui.Warn("The web UI is exposed on every host interface. Restrict it with a firewall, VPN, or reverse proxy. API and Postgres stay on 127.0.0.1.")
	} else {
		rt.ui.Warn("The web UI is bound to localhost. Use an SSH tunnel or a host reverse proxy for remote access.")
	}
	return nil
}

func (rt runtime) listBackups() error {
	files, err := rt.manager.ListBackups()
	if err != nil {
		return err
	}
	rt.ui.Section("Backups")
	if len(files) == 0 {
		rt.ui.Info("No backups found")
		return nil
	}
	for _, file := range files {
		fmt.Fprintln(rt.app.Out, "  "+file)
	}
	return nil
}

func (rt runtime) withSuccess(message string, err error) error {
	if err != nil {
		return err
	}
	rt.ui.Success("%s", message)
	return nil
}

func (app *App) printHelp() {
	fmt.Fprint(app.Out, `Rakazo Manager — small, durable Docker lifecycle manager

Usage:
  rakazo-manager [--yes] [--no-color] COMMAND [OPTIONS] [FOLDER]

Global options (must appear before COMMAND):
  --yes                           Confirm ordinary guarded operations
  --no-color                      Disable ANSI styling
  -h, --help                      Show this help

Commands:
  install [options] [folder]      Create, repair, or preview an instance
  instances list [--json] [parent]
                                  Discover managed instances in immediate children
  config show|set [options]       Inspect or safely change managed configuration
  import-instance [options] EXPORT.zip [folder]
                                  Recover a complete disaster-recovery export
  start [--rebuild] [folder]      Start the instance
  stop [folder]                   Stop without deleting data
  restart [folder]                Restart the stack
  status [--json] [folder]        Show ports, paths, image, and container state
  logs [--tail N] [folder]        Follow container logs
  shell [folder] [-- COMMAND...]  Interactive shell inside the API container
  open [folder]                   Show the local web and health URLs
  backup [--label NAME] [folder]  Create and verify a Postgres + data backup
  backups [--json] [folder]       List backups
  backups prune [--keep N] [--dry-run] [folder]
                                  Remove old automatic safety backups
  export-instance [--without-workspace] [folder]
                                  Export data, workspace, and recovery metadata
  restore [--dry-run] BACKUP.zip [folder]
                                  Restore after making a safety backup
  update [--dry-run] [folder]     Backup, pull GHCR, recreate, and verify
  update check [--json] [folder]  Check the registry without pulling or recreating
  rollback [folder]               Recreate with the previous image
  schedule set|show|remove|run    Manage scheduled backups and retention
  decommission [options] [folder] Remove containers/files; retain data by default
  doctor [--json] [--clear-stale-lock] [folder]
                                  Validate paths, mounts, Docker, and API health
  self-update [--check] [--json]  Update or inspect the Rakazo Manager CLI release
  completion bash|zsh|fish        Generate shell completion
  man                             Generate a roff manual page
  menu [folder]                   Open the interactive menu
  version [--json]                Show build information

Install options:
  --name NAME                    Override the derived instance name
  --image IMAGE                  Override ghcr.io/elie222/rakazo/app:edge
  --web-port PORT                Host port; 0 auto-detects a free port
  --api-port PORT                Host port; 0 auto-detects a free port
  --postgres-port PORT           Host port; 0 auto-detects a free port
  --bind-all                     Publish the web UI on 0.0.0.0; API and Postgres stay on 127.0.0.1
  --origin URL                   IP, domain, or URL (IP becomes http://IP:<web-port>)
  --no-pull                      Generate without pulling the image
  --no-start                     Generate without starting the stack
  --rebuild-on-start             Rebuild docker-compose.yml on every start
  --openrouter-key-file FILE     Optional OpenRouter API key ('-' reads stdin)
  --dry-run                      Print ports, Compose, and actions without changes

Examples:
  rakazo-manager install ./stack
  rakazo-manager start ./stack
  rakazo-manager open ./stack
  rakazo-manager doctor ./stack
  rakazo-manager update ./stack
`)
}
