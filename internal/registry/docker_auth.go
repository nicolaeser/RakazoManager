package registry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Auth struct {
	Username string
	Password string
}

type AuthProvider interface {
	AuthFor(registryHost string) (Auth, bool, error)
}

type DockerConfigAuth struct {
	Path string
}

func (provider DockerConfigAuth) AuthFor(registryHost string) (Auth, bool, error) {
	path := strings.TrimSpace(provider.Path)
	if path == "" {
		path = defaultDockerConfigPath()
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Auth{}, false, nil
		}
		return Auth{}, false, fmt.Errorf("read Docker config: %w", err)
	}
	var file struct {
		Auths map[string]struct {
			Auth     string `json:"auth"`
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(content, &file); err != nil {
		return Auth{}, false, fmt.Errorf("parse Docker config: %w", err)
	}
	if len(file.Auths) == 0 {
		return Auth{}, false, nil
	}
	for _, key := range dockerAuthLookupKeys(registryHost) {
		entry, ok := file.Auths[key]
		if !ok {
			continue
		}
		if entry.Username != "" || entry.Password != "" {
			return Auth{Username: entry.Username, Password: entry.Password}, true, nil
		}
		if strings.TrimSpace(entry.Auth) == "" {
			continue
		}
		decoded, decodeErr := base64.StdEncoding.DecodeString(entry.Auth)
		if decodeErr != nil {

			decoded, decodeErr = base64.RawStdEncoding.DecodeString(entry.Auth)
			if decodeErr != nil {
				return Auth{}, false, fmt.Errorf("decode Docker auth for %s: %w", key, decodeErr)
			}
		}
		username, password, found := strings.Cut(string(decoded), ":")
		if !found {
			return Auth{}, false, fmt.Errorf("Docker auth for %s is not username:password", key)
		}
		return Auth{Username: username, Password: password}, true, nil
	}
	return Auth{}, false, nil
}

func defaultDockerConfigPath() string {
	if configured := strings.TrimSpace(os.Getenv("DOCKER_CONFIG")); configured != "" {
		return filepath.Join(configured, "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".docker", "config.json")
	}
	return filepath.Join(home, ".docker", "config.json")
}

func dockerAuthLookupKeys(registryHost string) []string {
	host := strings.TrimSpace(strings.ToLower(registryHost))
	keys := []string{host, "https://" + host, "https://" + host + "/", "http://" + host, "http://" + host + "/"}
	if host == dockerHubRegistry || host == "docker.io" || host == "index.docker.io" {
		keys = append(keys,
			"https://index.docker.io/v1/",
			"https://index.docker.io/v1",
			"index.docker.io",
			"docker.io",
			dockerHubRegistry,
		)
	}
	return keys
}
