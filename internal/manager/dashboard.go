package manager

import (
	"context"
	"fmt"

	"github.com/nicolaeser/RakazoManager/internal/secrets"
)

func (manager *Manager) SetOpenRouterAPIKey(ctx context.Context, key string) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	lock, err := manager.operationLock("openrouter-key")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	_, values, err := manager.Load()
	if err != nil {
		return err
	}
	previous := cloneSecrets(values)
	if err := manager.SecretStore.SetOptional(values, secrets.OpenRouterAPIKey, key); err != nil {
		return err
	}
	running, err := manager.Docker.ServiceRunningStatus(ctx)
	if err != nil {
		_ = manager.SecretStore.Save(previous)
		return err
	}
	if running {
		if err := manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "api", "worker", "web"); err != nil {
			_ = manager.SecretStore.Save(previous)
			_ = manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "api", "worker", "web")
			return fmt.Errorf("apply OpenRouter key; previous secrets restored: %w", err)
		}
		if _, err := manager.waitForAPI(ctx, apiReadyTimeout); err != nil {
			_ = manager.SecretStore.Save(previous)
			_ = manager.Docker.Compose(ctx, false, "up", "-d", "--force-recreate", "api", "worker", "web")
			return fmt.Errorf("verify stack after OpenRouter key change; previous secrets restored: %w", err)
		}
	}
	_ = manager.StateStore.Log("openrouter-key", "result=success")
	return nil
}

func cloneSecrets(values secrets.Values) secrets.Values {
	cloned := make(secrets.Values, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
