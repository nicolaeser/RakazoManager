package manager

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/fsutil"
	"github.com/nicolaeser/RakazoManager/internal/ports"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
)

type InstallOptions struct {
	Name                     string
	Image                    string
	WebPort                  int
	APIPort                  int
	PostgresPort             int
	BindAll                  bool
	Origin                   string
	Pull                     bool
	Start                    bool
	RebuildComposeOnStart    bool
	HasRebuildComposeOnStart bool
	OpenRouterAPIKey         string
}

type StartOptions struct {
	Rebuild bool
}

type InstallResult struct {
	Config  config.Config
	Created bool
}

type Status struct {
	Root                  string
	Name                  string
	Image                 string
	TrackedImage          string
	WebURL                string
	WebPort               int
	APIPort               int
	PostgresPort          int
	BindAddress           string
	RebuildComposeOnStart bool
	Data                  string
	AppData               string
	PostgresData          string
	Workspace             string
	Backups               string
	Containers            string
	Version               string
	APIHealthy            bool
	APIInfo               string
}

type Access struct {
	URL     string
	API     string
	Listens string
}

func (manager *Manager) Install(ctx context.Context, options InstallOptions) (result InstallResult, operationErr error) {
	lock, err := manager.operationLock("install")
	if err != nil {
		return result, err
	}
	defer releaseLock(lock, &operationErr)

	var cfg config.Config
	created := !manager.ConfigStore.Exists()
	if created {
		if fsutil.FileExists(manager.Paths.Compose) || fsutil.FileExists(manager.Paths.LegacyCompose()) {
			return result, fmt.Errorf("%s already exists but is not owned by Rakazo Manager", manager.Paths.Compose)
		}
		manager.progress("Creating instance metadata and selecting free ports")
		bindAddress := config.DefaultBindAddress
		if options.BindAll {
			bindAddress = config.PublicBindAddress
		}
		webPort, apiPort, postgresPort, err := ports.Select(manager.Paths.Root, bindAddress, options.WebPort, options.APIPort, options.PostgresPort)
		if err != nil {
			return result, err
		}
		manager.progress("Using host ports: web %d, API %d, Postgres %d (bind %s)", webPort, apiPort, postgresPort, bindAddress)
		cfg = config.New(manager.Paths.Root, options.Name, options.Image, webPort, apiPort, postgresPort)
		cfg.BindAddress = bindAddress
		origin, originErr := config.ResolveOrigin(options.Origin, webPort)
		if originErr != nil {
			return result, originErr
		}
		cfg.Origin = origin
		if err := validateUniqueInstanceName(manager.Paths.Root, cfg.Name); err != nil {
			return result, err
		}
		if options.HasRebuildComposeOnStart {
			cfg.RebuildComposeOnStart = options.RebuildComposeOnStart
		}
		if err := manager.ConfigStore.Save(cfg); err != nil {
			return result, err
		}
	} else {
		manager.progress("Repairing existing instance (name and ports stay stable)")
		cfg, err = manager.ConfigStore.Load()
		if err != nil {
			return result, err
		}
		if options.Name != "" || options.Image != "" || options.WebPort != 0 || options.APIPort != 0 || options.PostgresPort != 0 || options.BindAll || options.Origin != "" {
			fmt.Fprintln(manager.Err, "note: install options are ignored for an existing instance; its name and ports remain stable")
		}
		if options.HasRebuildComposeOnStart {
			cfg.RebuildComposeOnStart = options.RebuildComposeOnStart
		}
		if err := manager.ConfigStore.Save(cfg); err != nil {
			return result, err
		}
	}

	manager.progress("Writing secrets and generating Compose (./pg, ./data, ./backups)")
	values, _, err := manager.SecretStore.LoadOrCreate()
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(options.OpenRouterAPIKey) != "" {
		if err := manager.SecretStore.SetOptional(values, secrets.OpenRouterAPIKey, options.OpenRouterAPIKey); err != nil {
			return result, err
		}
	}
	if err := manager.Generator.Prepare(cfg, values, true); err != nil {
		return result, err
	}
	manager.progress("Checking Docker CLI and Compose file")
	if err := manager.Docker.CheckCLI(ctx); err != nil {
		return result, err
	}
	if err := manager.Docker.ValidateCompose(ctx); err != nil {
		return result, fmt.Errorf("generated Compose configuration is invalid: %w", err)
	}
	if options.Pull || options.Start {
		if err := manager.Docker.CheckDaemon(ctx); err != nil {
			return result, err
		}
	}
	if options.Pull {
		manager.progress("Pulling GHCR app image (linux/amd64; this can take a while)")
		if err := manager.Docker.Compose(ctx, false, "pull"); err != nil {
			return result, fmt.Errorf("pull Rakazo images: %w", err)
		}
	}
	if options.Start {
		manager.progress("Starting Postgres, API, worker, web, and supervisor")
		upArgs := []string{"up", "-d", "--wait", "--wait-timeout", "300"}
		if !options.Pull {
			upArgs = append(upArgs, "--pull", "never")
		}
		if err := manager.Docker.Compose(ctx, false, upArgs...); err != nil {
			return result, fmt.Errorf("start Rakazo: %w", err)
		}
		if err := manager.verifyStartedInstance(ctx); err != nil {
			return result, err
		}
	}
	_ = manager.StateStore.Log("install", fmt.Sprintf("created=%t result=success", created))
	return InstallResult{Config: cfg, Created: created}, nil
}

func (manager *Manager) Start(ctx context.Context, options StartOptions) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	cfg, values, err := manager.Load()
	if err != nil {
		return err
	}
	force := options.Rebuild || cfg.RebuildComposeOnStart
	if force {
		manager.progress("Rebuilding docker-compose.yml from managed template")
	}
	if err := manager.Generator.Prepare(cfg, values, force); err != nil {
		return err
	}
	if err := manager.Docker.CheckCLI(ctx); err != nil {
		return err
	}
	if err := manager.Docker.ValidateCompose(ctx); err != nil {
		return fmt.Errorf("validate generated Compose configuration: %w", err)
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("start")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		return err
	}
	if err := manager.verifyStartedInstance(ctx); err != nil {
		return err
	}
	_ = manager.StateStore.Log("start", fmt.Sprintf("rebuild=%t result=success", force))
	return nil
}

func (manager *Manager) verifyStartedInstance(ctx context.Context) error {
	if err := manager.verifyContainerBinds(ctx); err != nil {
		return fmt.Errorf("stack started but bind mounts are wrong: %w", err)
	}
	if _, err := manager.waitForAPI(ctx, apiReadyTimeout); err != nil {
		return fmt.Errorf("stack started but API health verification failed: %w", err)
	}
	return nil
}

func (manager *Manager) Stop(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("stop")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.Compose(ctx, false, "stop"); err != nil {
		return err
	}
	_ = manager.StateStore.Log("stop", "result=success")
	return nil
}

func (manager *Manager) Restart(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("restart")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if err := manager.Docker.Compose(ctx, false, "restart"); err != nil {
		return err
	}
	if err := manager.verifyStartedInstance(ctx); err != nil {
		return err
	}
	_ = manager.StateStore.Log("restart", "result=success")
	return nil
}

func (manager *Manager) Status(ctx context.Context) (Status, error) {
	if err := manager.RequireInstalled(); err != nil {
		return Status{}, err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Root:                  manager.Paths.Root,
		Name:                  cfg.Name,
		Image:                 cfg.EffectiveImage(),
		TrackedImage:          cfg.Image,
		WebURL:                cfg.PublicOrigin(),
		WebPort:               cfg.WebPort,
		APIPort:               cfg.APIPort,
		PostgresPort:          cfg.PostgresPort,
		BindAddress:           cfg.BindAddress,
		RebuildComposeOnStart: cfg.RebuildComposeOnStart,
		Data:                  manager.Paths.Data,
		AppData:               manager.Paths.App,
		PostgresData:          manager.Paths.Postgres,
		Workspace:             manager.Paths.Workspace,
		Backups:               manager.Paths.Backups,
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		status.Containers = "Docker unavailable: " + err.Error()
		return status, nil
	}
	status.Containers, err = manager.Docker.ComposeOutput(ctx, "ps")
	if err != nil {
		return Status{}, err
	}
	if manager.Docker.ServiceRunning(ctx) {
		health, healthErr := manager.APIHealth(ctx)
		if healthErr != nil {
			status.APIInfo = healthErr.Error()
		} else {
			status.APIHealthy = true
			status.Version = health.Revision
			status.APIInfo = fmt.Sprintf("healthy runtime=%s sandbox=%s jobs=%s", health.Runtime, health.Sandbox, health.Jobs)
		}
	}
	return status, nil
}

func (manager *Manager) Access() (Access, error) {
	cfg, _, err := manager.LoadExisting()
	if err != nil {
		return Access{}, err
	}
	return Access{
		URL:     cfg.PublicOrigin(),
		API:     fmt.Sprintf("http://127.0.0.1:%d/health", cfg.APIPort),
		Listens: fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.WebPort),
	}, nil
}

func (manager *Manager) Logs(ctx context.Context, tail int) error {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if tail < 1 {
		tail = 100
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := manager.Docker.Compose(runCtx, true, "logs", "-f", "--tail", fmt.Sprintf("%d", tail))
	if command.IsInterrupted(err) {
		return nil
	}
	return err
}

func (manager *Manager) Shell(ctx context.Context, commandArgs ...string) error {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	running, err := manager.Docker.ServiceRunningStatus(ctx)
	if err != nil {
		return err
	}
	if !running {
		return fmt.Errorf("Rakazo API is not running; start it first: rakazo-manager start %q", manager.Paths.Root)
	}
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	err = manager.Docker.Shell(ctx, commandArgs...)
	if command.IsInterrupted(err) {
		return nil
	}
	return err
}

func (manager *Manager) IsInstalled() bool {
	return manager.Docker.IsInstalled()
}

func SanitizeLabel(value string) string {
	value = strings.TrimSpace(value)
	var output strings.Builder
	lastDash := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '.' || character == '_' || character == '-'
		if valid {
			output.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			output.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(output.String(), "-")
}
