package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/fsutil"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

const (
	SchemaVersion      = 1
	DefaultImage       = "ghcr.io/elie222/rakazo/app:edge"
	DefaultBindAddress = "127.0.0.1"
	PublicBindAddress  = "0.0.0.0"
	AppPlatform        = "linux/amd64"
)

type Config struct {
	SchemaVersion         int    `json:"schema_version"`
	Name                  string `json:"name"`
	Image                 string `json:"image"`
	PinnedImage           string `json:"pinned_image,omitempty"`
	BindAddress           string `json:"bind_address"`
	Origin                string `json:"origin,omitempty"`
	WebPort               int    `json:"web_port"`
	APIPort               int    `json:"api_port"`
	PostgresPort          int    `json:"postgres_port"`
	RebuildComposeOnStart bool   `json:"rebuild_compose_on_start"`
}

type Store struct {
	Paths stack.Paths
}

func New(root, name, image string, webPort, apiPort, postgresPort int) Config {
	if strings.TrimSpace(name) == "" {
		name = InstanceName(root)
	}
	if strings.TrimSpace(image) == "" {
		image = DefaultImage
	}
	return Config{
		SchemaVersion: SchemaVersion,
		Name:          name,
		Image:         image,
		BindAddress:   DefaultBindAddress,
		WebPort:       webPort,
		APIPort:       apiPort,
		PostgresPort:  postgresPort,
	}
}

func (cfg Config) EffectiveImage() string {
	if cfg.PinnedImage != "" {
		return cfg.PinnedImage
	}
	return cfg.Image
}

func (cfg Config) PublicOrigin() string {
	if cfg.Origin != "" {
		return cfg.Origin
	}
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.WebPort)
}

func ResolveOrigin(value string, webPort int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, " \t") {
		return "", fmt.Errorf("origin must not contain whitespace")
	}
	if strings.Contains(value, "://") {
		return NormalizeOrigin(value)
	}
	if strings.ContainsAny(value, "/?#") {
		return "", fmt.Errorf("origin must be a host or http(s) URL without a path")
	}

	host := value
	port := 0
	if h, p, err := net.SplitHostPort(value); err == nil {
		host = h
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("origin port is invalid")
		}
	}

	if ip := net.ParseIP(host); ip != nil {
		if host == PublicBindAddress {
			return "", fmt.Errorf("origin host cannot be %s", PublicBindAddress)
		}
		if port == 0 {
			if webPort < 1 || webPort > 65535 {
				return "", fmt.Errorf("origin IP %s needs the web port", host)
			}
			port = webPort
		}
		return NormalizeOrigin("http://" + net.JoinHostPort(host, strconv.Itoa(port)))
	}

	if port != 0 {
		return NormalizeOrigin("https://" + net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return NormalizeOrigin("https://" + host)
}

func NormalizeOrigin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, " \t") {
		return "", fmt.Errorf("origin must not contain whitespace")
	}
	if !strings.Contains(value, "://") {
		if strings.ContainsAny(value, "/?#") {
			return "", fmt.Errorf("origin must be a host or http(s) URL without a path")
		}
		value = "https://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("origin is not a valid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("origin must use http or https")
	}
	if parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("origin must include a host and no userinfo")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", fmt.Errorf("origin must not include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("origin must not include a query or fragment")
	}
	if parsed.Hostname() == PublicBindAddress {
		return "", fmt.Errorf("origin host cannot be %s", PublicBindAddress)
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func (s Store) Load() (Config, error) {
	content, err := os.ReadFile(s.Paths.Config)
	if err != nil {
		return Config{}, fmt.Errorf("read instance metadata: %w", err)
	}
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", s.Paths.Config, err)
	}
	if cfg.BindAddress == "" {
		cfg.BindAddress = DefaultBindAddress
	}
	if cfg.Origin != "" {
		origin, originErr := NormalizeOrigin(cfg.Origin)
		if originErr != nil {
			return Config{}, fmt.Errorf("validate instance metadata: %w", originErr)
		}
		cfg.Origin = origin
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate instance metadata: %w", err)
	}
	return cfg, nil
}

func (s Store) Save(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refuse invalid instance metadata: %w", err)
	}
	content, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instance metadata: %w", err)
	}
	content = append(content, '\n')
	if err := fsutil.AtomicWriteFile(s.Paths.Config, content, 0o600); err != nil {
		return fmt.Errorf("save instance metadata: %w", err)
	}
	return nil
}

func (s Store) Exists() bool {
	return fsutil.FileExists(s.Paths.Config)
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)
)

func (cfg Config) Validate() error {
	var problems []string
	if cfg.SchemaVersion != SchemaVersion {
		problems = append(problems, fmt.Sprintf("schema_version must be %d", SchemaVersion))
	}
	if !namePattern.MatchString(cfg.Name) {
		problems = append(problems, "name must contain only lowercase letters, numbers, and hyphens (maximum 63 characters)")
	}
	if !imagePattern.MatchString(cfg.Image) {
		problems = append(problems, "image contains unsupported characters")
	}
	if cfg.PinnedImage != "" && !imagePattern.MatchString(cfg.PinnedImage) {
		problems = append(problems, "pinned_image contains unsupported characters")
	}
	if cfg.BindAddress != DefaultBindAddress && cfg.BindAddress != PublicBindAddress {
		problems = append(problems, "bind_address must be 127.0.0.1 or 0.0.0.0")
	}
	if cfg.Origin != "" {
		origin, err := NormalizeOrigin(cfg.Origin)
		if err != nil {
			problems = append(problems, err.Error())
		} else if origin != cfg.Origin {
			problems = append(problems, "origin must be a canonical http(s) URL without a path")
		}
	}
	if !validPort(cfg.WebPort) {
		problems = append(problems, "web_port must be between 1 and 65535")
	}
	if !validPort(cfg.APIPort) {
		problems = append(problems, "api_port must be between 1 and 65535")
	}
	if !validPort(cfg.PostgresPort) {
		problems = append(problems, "postgres_port must be between 1 and 65535")
	}
	seen := map[int]string{
		cfg.WebPort:      "web_port",
		cfg.APIPort:      "api_port",
		cfg.PostgresPort: "postgres_port",
	}
	if len(seen) != 3 {
		problems = append(problems, "web_port, api_port, and postgres_port must all differ")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

func InstanceName(root string) string {
	absolute, err := filepath.Abs(root)
	if err != nil {
		absolute = filepath.Clean(root)
	}
	base := slug(filepath.Base(absolute))
	if base == "" {
		base = "instance"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(absolute)))
	suffix := hex.EncodeToString(sum[:4])
	const prefix = "rakazo-"
	maxBase := 63 - len(prefix) - 1 - len(suffix)
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
	}
	return prefix + base + "-" + suffix
}

func FolderHash(root string) uint16 {
	absolute, err := filepath.Abs(root)
	if err != nil {
		absolute = filepath.Clean(root)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(absolute)))
	return uint16(sum[0])<<8 | uint16(sum[1])
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	lastDash := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
		if valid {
			result.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(result.String(), "-")
}
