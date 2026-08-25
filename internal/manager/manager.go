package manager

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/docker"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
	"github.com/nicolaeser/RakazoManager/internal/stack"
	"github.com/nicolaeser/RakazoManager/internal/state"
)

type ProgressFunc func(format string, args ...any)

type Manager struct {
	Paths       stack.Paths
	ConfigStore config.Store
	SecretStore secrets.Store
	StateStore  state.Store
	Generator   compose.Generator
	Docker      docker.Client
	Out         io.Writer
	Err         io.Writer

	DashboardWait func(context.Context, time.Duration) (DashboardHealth, error)

	Progress ProgressFunc
}

func New(paths stack.Paths, runner command.Runner, in io.Reader, out, errOut io.Writer) *Manager {
	return &Manager{
		Paths:       paths,
		ConfigStore: config.Store{Paths: paths},
		SecretStore: secrets.Store{Paths: paths},
		StateStore:  state.Store{Paths: paths},
		Generator:   compose.Generator{Paths: paths},
		Docker:      docker.New(paths, runner, in, out, errOut),
		Out:         out,
		Err:         errOut,
	}
}

func (manager *Manager) progress(format string, args ...any) {
	if manager.Progress != nil {
		manager.Progress(format, args...)
	}
}

func (manager *Manager) RequireInstalled() error {
	if !manager.Docker.IsInstalled() {
		return fmt.Errorf("no Rakazo instance found at %s; run: rakazo-manager install %q", manager.Paths.Root, manager.Paths.Root)
	}
	return nil
}

func (manager *Manager) Load() (config.Config, secrets.Values, error) {
	if err := manager.RequireInstalled(); err != nil {
		return config.Config{}, nil, err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	values, _, err := manager.SecretStore.LoadOrCreate()
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, values, nil
}

func (manager *Manager) LoadExisting() (config.Config, secrets.Values, error) {
	if err := manager.RequireInstalled(); err != nil {
		return config.Config{}, nil, err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	values, err := manager.SecretStore.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, values, nil
}

func (manager *Manager) Prepare(ctx context.Context, validate bool) error {
	cfg, values, err := manager.Load()
	if err != nil {
		return err
	}
	if err := manager.Generator.Prepare(cfg, values, false); err != nil {
		return err
	}
	if validate {
		if err := manager.Docker.CheckCLI(ctx); err != nil {
			return err
		}
		if err := manager.Docker.ValidateCompose(ctx); err != nil {
			return fmt.Errorf("validate generated Compose configuration: %w", err)
		}
	}
	return nil
}

func (manager *Manager) saveAndPrepare(cfg config.Config) error {
	values, _, err := manager.SecretStore.LoadOrCreate()
	if err != nil {
		return err
	}
	if err := manager.ConfigStore.Save(cfg); err != nil {
		return err
	}
	return manager.Generator.Prepare(cfg, values, false)
}

func (manager *Manager) ensureRunning(ctx context.Context) error {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	if err := manager.Docker.EnsureRunning(ctx); err != nil {
		return err
	}
	return nil
}

func (manager *Manager) operationLock(operation string) (*state.Lock, error) {
	return manager.StateStore.Acquire(operation)
}

func releaseLock(lock *state.Lock, operationErr *error) {
	if err := lock.Release(); err != nil && *operationErr == nil {
		*operationErr = err
	}
}
