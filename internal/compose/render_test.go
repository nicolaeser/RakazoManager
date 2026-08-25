package compose

import (
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/config"
)

func TestRenderBindAllPublishesOnlyWeb(t *testing.T) {
	cfg := config.New("/tmp/rakazo-stack", "rakazo-test", config.DefaultImage, 5173, 3100, 5433)
	cfg.BindAddress = config.PublicBindAddress
	rendered := string(Render(cfg))

	if !strings.Contains(rendered, `"0.0.0.0:5173:5173"`) {
		t.Fatalf("web should publish on 0.0.0.0:\n%s", rendered)
	}
	if !strings.Contains(rendered, `"127.0.0.1:3100:3100"`) {
		t.Fatalf("API should stay on 127.0.0.1:\n%s", rendered)
	}
	if !strings.Contains(rendered, `"127.0.0.1:5433:5432"`) {
		t.Fatalf("Postgres should stay on 127.0.0.1:\n%s", rendered)
	}
	if strings.Contains(rendered, `"0.0.0.0:3100:3100"`) || strings.Contains(rendered, `"0.0.0.0:5433:5432"`) {
		t.Fatal("API and Postgres must stay on 127.0.0.1 when bind-all is set")
	}
	if strings.Contains(rendered, "${") {
		t.Fatal("generated Compose must not interpolate env vars")
	}
}

func TestRenderUsesConfiguredOrigin(t *testing.T) {
	cfg := config.New("/tmp/rakazo-stack", "rakazo-test", config.DefaultImage, 5173, 3100, 5433)
	cfg.Origin = "https://rakazo.example.com"
	rendered := string(Render(cfg))
	if !strings.Contains(rendered, `BETTER_AUTH_URL: "https://rakazo.example.com"`) {
		t.Fatalf("missing BETTER_AUTH_URL:\n%s", rendered)
	}
	if !strings.Contains(rendered, `WEB_ORIGIN: "https://rakazo.example.com"`) {
		t.Fatalf("missing WEB_ORIGIN:\n%s", rendered)
	}
	if !strings.Contains(rendered, `API_URL: "https://rakazo.example.com"`) {
		t.Fatalf("missing API_URL:\n%s", rendered)
	}
	if strings.Contains(rendered, `BETTER_AUTH_URL: "http://127.0.0.1:5173"`) {
		t.Fatal("localhost origin should not be used when a public origin is set")
	}
	if !strings.Contains(rendered, `RAKAZO_HOST: "rakazo.example.com"`) {
		t.Fatalf("web RAKAZO_HOST should follow the origin host:\n%s", rendered)
	}
}

func TestSyncManagedKeepsAPIAndPostgresLoopback(t *testing.T) {
	cfg := config.New("/tmp/rakazo-stack", "rakazo-test", config.DefaultImage, 5173, 3100, 5433)
	existing := Render(cfg)
	cfg.BindAddress = config.PublicBindAddress
	cfg.WebPort = 8080
	synced := string(SyncManaged(existing, cfg))
	if !strings.Contains(synced, `"0.0.0.0:8080:5173"`) {
		t.Fatalf("sync should move web to 0.0.0.0:8080:\n%s", synced)
	}
	if !strings.Contains(synced, `"127.0.0.1:3100:3100"`) || !strings.Contains(synced, `"127.0.0.1:5433:5432"`) {
		t.Fatalf("sync should keep API and Postgres on 127.0.0.1:\n%s", synced)
	}
}
