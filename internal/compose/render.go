package compose

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/fsutil"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

const runtimeUID = 1000

type Generator struct {
	Paths stack.Paths
}

func (generator Generator) Prepare(cfg config.Config, secretValues secrets.Values, force bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, key := range []string{
		secrets.PostgresPassword,
		secrets.DatabaseURL,
		secrets.BetterAuthSecret,
		secrets.EncryptionKey,
		secrets.SupervisorToken,
	} {
		if secretValues[key] == "" {
			return fmt.Errorf("required secret %s is missing", key)
		}
	}

	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{generator.Paths.Manager, 0o700},
		{generator.Paths.Data, 0o700},
		{generator.Paths.Postgres, 0o700},
		{generator.Paths.App, 0o700},
		{generator.Paths.Workspace, 0o755},
		{generator.Paths.Backups, 0o700},
	} {
		if err := ensureDirectory(directory.path, directory.mode); err != nil {
			return err
		}
	}

	desired := Render(cfg)
	existingPath := ""
	if fsutil.FileExists(generator.Paths.Compose) {
		existingPath = generator.Paths.Compose
	} else if fsutil.FileExists(generator.Paths.LegacyCompose()) {
		existingPath = generator.Paths.LegacyCompose()
	}

	if existingPath == "" {
		if err := fsutil.AtomicWriteFile(generator.Paths.Compose, desired, 0o600); err != nil {
			return fmt.Errorf("write Compose file: %w", err)
		}
		return nil
	}

	existing, err := os.ReadFile(existingPath)
	if err != nil {
		return fmt.Errorf("read Compose file: %w", err)
	}

	var content []byte
	switch {
	case force:
		content = desired
	case bytes.Equal(existing, desired):
		content = existing
	default:
		content = SyncManaged(existing, cfg)
	}

	if existingPath != generator.Paths.Compose || !bytes.Equal(existing, content) {
		if err := fsutil.AtomicWriteFile(generator.Paths.Compose, content, 0o600); err != nil {
			return fmt.Errorf("write Compose file: %w", err)
		}
	}
	if existingPath == generator.Paths.LegacyCompose() || fsutil.FileExists(generator.Paths.LegacyCompose()) {
		_ = os.Remove(generator.Paths.LegacyCompose())
	}
	return nil
}

func Render(cfg config.Config) []byte {
	image := yamlQuote(cfg.EffectiveImage())
	origin := yamlQuote(cfg.PublicOrigin())
	apiProxy := `"http://api:3100"`
	webPublish := yamlQuote(fmt.Sprintf("%s:%d:5173", cfg.BindAddress, cfg.WebPort))
	apiPublish := yamlQuote(fmt.Sprintf("%s:%d:3100", config.DefaultBindAddress, cfg.APIPort))
	postgresPublish := yamlQuote(fmt.Sprintf("%s:%d:5432", config.DefaultBindAddress, cfg.PostgresPort))

	var output strings.Builder
	fmt.Fprintf(&output, "name: %s\n", yamlQuote(cfg.Name))
	output.WriteString("\n")
	output.WriteString("services:\n")

	output.WriteString("  postgres:\n")
	output.WriteString("    image: postgres:16\n")
	output.WriteString("    restart: unless-stopped\n")
	output.WriteString("    environment:\n")
	output.WriteString("      POSTGRES_USER: rakazo\n")
	output.WriteString("      POSTGRES_DB: rakazo\n")
	output.WriteString("    env_file:\n")
	output.WriteString("      - ./.manager/secrets.env\n")
	output.WriteString("    ports:\n")
	fmt.Fprintf(&output, "      - %s\n", postgresPublish)
	output.WriteString("    volumes:\n")
	output.WriteString("      - ./pg:/var/lib/postgresql/data\n")
	output.WriteString("      - ./backups:/backups\n")
	output.WriteString("    healthcheck:\n")
	output.WriteString("      test: [\"CMD-SHELL\", \"pg_isready -U rakazo -d rakazo\"]\n")
	output.WriteString("      interval: 5s\n")
	output.WriteString("      timeout: 5s\n")
	output.WriteString("      retries: 20\n")

	output.WriteString("  data-init:\n")
	output.WriteString("    image: busybox:latest\n")
	output.WriteString("    restart: \"no\"\n")
	fmt.Fprintf(&output, "    command: [\"chown\", \"-R\", \"%d:%d\", \"/data\"]\n", runtimeUID, runtimeUID)
	output.WriteString("    volumes:\n")
	output.WriteString("      - ./data:/data\n")

	writeAppService(&output, "supervisor", image)
	output.WriteString("    user: root\n")
	output.WriteString("    command: [\"pnpm\", \"--filter\", \"@rakazo/sandbox-supervisor\", \"start\"]\n")
	output.WriteString("    environment:\n")
	output.WriteString("      NODE_ENV: production\n")
	output.WriteString("      DATA_DIR: /data\n")
	output.WriteString("      SUPERVISOR_HOST: \"0.0.0.0\"\n")
	output.WriteString("      SUPERVISOR_PORT: \"7091\"\n")
	output.WriteString("      DOCKER_SOCKET: /var/run/docker.sock\n")
	output.WriteString("      RAKAZO_COMPUTER_IMAGE: rakazo/computer:local\n")
	output.WriteString("      RAKAZO_COMPUTER_CONTEXT: /app/infra/sandboxes/computer\n")
	output.WriteString("      SANDBOX_SCREEN_NETWORK: internal\n")
	output.WriteString("    env_file:\n")
	output.WriteString("      - ./.manager/secrets.env\n")
	output.WriteString("    volumes:\n")
	output.WriteString("      - /var/run/docker.sock:/var/run/docker.sock\n")
	output.WriteString("      - ./data:/data\n")
	output.WriteString("    depends_on:\n")
	output.WriteString("      data-init:\n")
	output.WriteString("        condition: service_completed_successfully\n")
	output.WriteString("    healthcheck:\n")
	output.WriteString("      test:\n")
	output.WriteString("        - CMD\n")
	output.WriteString("        - node\n")
	output.WriteString("        - -e\n")
	output.WriteString("        - fetch('http://127.0.0.1:7091/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))\n")
	output.WriteString("      interval: 5s\n")
	output.WriteString("      timeout: 5s\n")
	output.WriteString("      retries: 30\n")

	writeAppService(&output, "api", image)
	output.WriteString("    command:\n")
	output.WriteString("      - bash\n")
	output.WriteString("      - -lc\n")
	output.WriteString("      - pnpm --filter @rakazo/db exec prisma migrate deploy && pnpm --filter @rakazo/api start\n")
	writeAppEnvironment(&output, cfg, origin)
	output.WriteString("    ports:\n")
	fmt.Fprintf(&output, "      - %s\n", apiPublish)
	output.WriteString("    volumes:\n")
	output.WriteString("      - ./data:/data\n")
	output.WriteString("      - ./backups:/backups\n")
	output.WriteString("    depends_on:\n")
	output.WriteString("      postgres:\n")
	output.WriteString("        condition: service_healthy\n")
	output.WriteString("      data-init:\n")
	output.WriteString("        condition: service_completed_successfully\n")
	output.WriteString("      supervisor:\n")
	output.WriteString("        condition: service_healthy\n")
	output.WriteString("    healthcheck:\n")
	output.WriteString("      test:\n")
	output.WriteString("        - CMD\n")
	output.WriteString("        - node\n")
	output.WriteString("        - -e\n")
	output.WriteString("        - fetch('http://127.0.0.1:3100/health').then(r=>{if(!r.ok)process.exit(1)}).catch(()=>process.exit(1))\n")
	output.WriteString("      interval: 10s\n")
	output.WriteString("      timeout: 5s\n")
	output.WriteString("      retries: 30\n")
	output.WriteString("      start_period: 40s\n")

	writeAppService(&output, "worker", image)
	output.WriteString("    command: [\"pnpm\", \"--filter\", \"@rakazo/worker\", \"start\"]\n")
	writeAppEnvironment(&output, cfg, origin)
	output.WriteString("    volumes:\n")
	output.WriteString("      - ./data:/data\n")
	output.WriteString("    depends_on:\n")
	output.WriteString("      postgres:\n")
	output.WriteString("        condition: service_healthy\n")
	output.WriteString("      api:\n")
	output.WriteString("        condition: service_healthy\n")

	writeAppService(&output, "web", image)
	output.WriteString("    command: [\"pnpm\", \"--filter\", \"@rakazo/web\", \"preview\", \"--host\", \"0.0.0.0\", \"--port\", \"5173\"]\n")
	output.WriteString("    environment:\n")
	output.WriteString("      NODE_ENV: production\n")
	fmt.Fprintf(&output, "      API_PROXY_TARGET: %s\n", apiProxy)
	fmt.Fprintf(&output, "      RAKAZO_HOST: %s\n", yamlQuote(cfg.HostName()))
	fmt.Fprintf(&output, "      BETTER_AUTH_URL: %s\n", origin)
	fmt.Fprintf(&output, "      WEB_ORIGIN: %s\n", origin)
	output.WriteString("    env_file:\n")
	output.WriteString("      - ./.manager/secrets.env\n")
	output.WriteString("    ports:\n")
	fmt.Fprintf(&output, "      - %s\n", webPublish)
	output.WriteString("    depends_on:\n")
	output.WriteString("      api:\n")
	output.WriteString("        condition: service_healthy\n")

	return []byte(output.String())
}

func writeAppService(output *strings.Builder, name, image string) {
	fmt.Fprintf(output, "  %s:\n", name)
	fmt.Fprintf(output, "    image: %s\n", image)
	fmt.Fprintf(output, "    platform: %s\n", config.AppPlatform)
	output.WriteString("    restart: unless-stopped\n")
}

func writeAppEnvironment(output *strings.Builder, cfg config.Config, origin string) {
	output.WriteString("    environment:\n")
	output.WriteString("      NODE_ENV: production\n")
	output.WriteString("      DATA_DIR: /data\n")
	fmt.Fprintf(output, "      BETTER_AUTH_URL: %s\n", origin)
	fmt.Fprintf(output, "      WEB_ORIGIN: %s\n", origin)
	fmt.Fprintf(output, "      API_URL: %s\n", origin)
	output.WriteString("      SANDBOX_SUPERVISOR_URL: http://supervisor:7091\n")
	output.WriteString("      SANDBOX_PROVIDER: docker\n")
	output.WriteString("      AGENT_RUNTIME: pi\n")
	output.WriteString("      WAKEUP_DRIVER: graphile\n")
	output.WriteString("      SIGNUPS_ENABLED: \"true\"\n")
	output.WriteString("    env_file:\n")
	output.WriteString("      - ./.manager/secrets.env\n")
}

func SyncManaged(existing []byte, cfg config.Config) []byte {
	text := string(existing)
	endsWithNL := strings.HasSuffix(text, "\n")
	if endsWithNL {
		text = strings.TrimSuffix(text, "\n")
	}
	lines := strings.Split(text, "\n")
	image := yamlQuote(cfg.EffectiveImage())
	name := yamlQuote(cfg.Name)
	origin := yamlQuote(cfg.PublicOrigin())
	hostName := yamlQuote(cfg.HostName())
	webPort := yamlQuote(fmt.Sprintf("%s:%d:5173", cfg.BindAddress, cfg.WebPort))
	apiPort := yamlQuote(fmt.Sprintf("%s:%d:3100", config.DefaultBindAddress, cfg.APIPort))
	postgresPort := yamlQuote(fmt.Sprintf("%s:%d:5432", config.DefaultBindAddress, cfg.PostgresPort))
	currentService := ""
	appServices := map[string]bool{"supervisor": true, "api": true, "worker": true, "web": true}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		indentLen := len(indent)
		if indentLen == 0 && strings.HasPrefix(trimmed, "name:") {
			lines[i] = "name: " + name
			continue
		}
		if indentLen == 2 && strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
			currentService = strings.TrimSuffix(trimmed, ":")
			continue
		}
		switch {
		case appServices[currentService] && strings.HasPrefix(trimmed, "image:"):
			lines[i] = indent + "image: " + image
		case isPublishedPort(trimmed, 5173):
			lines[i] = indent + "- " + webPort
		case isPublishedPort(trimmed, 3100):
			lines[i] = indent + "- " + apiPort
		case isPublishedPort(trimmed, 5432):
			lines[i] = indent + "- " + postgresPort
		case strings.HasPrefix(trimmed, "BETTER_AUTH_URL:"):
			lines[i] = indent + "BETTER_AUTH_URL: " + origin
		case strings.HasPrefix(trimmed, "WEB_ORIGIN:"):
			lines[i] = indent + "WEB_ORIGIN: " + origin
		case strings.HasPrefix(trimmed, "API_URL:"):
			lines[i] = indent + "API_URL: " + origin
		case strings.HasPrefix(trimmed, "RAKAZO_HOST:"):
			lines[i] = indent + "RAKAZO_HOST: " + hostName
		}
	}

	out := strings.Join(lines, "\n")
	if endsWithNL {
		out += "\n"
	}
	return []byte(out)
}

func isPublishedPort(trimmed string, containerPort int) bool {
	if !strings.HasPrefix(trimmed, "-") {
		return false
	}
	value := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
	value = strings.Trim(value, `"'`)
	if strings.Contains(value, "://") {
		return false
	}
	suffix := fmt.Sprintf(":%d", containerPort)
	if !strings.HasSuffix(value, suffix) {
		return false
	}
	return strings.Count(value, ":") >= 1
}

func ensureDirectory(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists but is not a directory", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return nil
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}
