package ports

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/nicolaeser/RakazoManager/internal/config"
)

const (
	searchWindow        = 2000
	defaultWebBase      = 5173
	defaultAPIBase      = 3100
	defaultPostgresBase = 5433
)

type Reservation struct {
	Port int
	Root string
	Role string
}

func Select(root, bindAddress string, requestedWeb, requestedAPI, requestedPostgres int) (int, int, int, error) {
	if bindAddress == "" {
		bindAddress = config.DefaultBindAddress
	}
	reserved := ReservedBySiblings(root)
	seed := int(config.FolderHash(root))
	webStart := defaultWebBase + seed%200
	apiStart := defaultAPIBase + seed%200
	postgresStart := defaultPostgresBase + seed%200

	web, err := selectOne(bindAddress, requestedWeb, webStart, nil, reserved)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("web port: %w", err)
	}
	api, err := selectOne(bindAddress, requestedAPI, apiStart, map[int]bool{web: true}, reserved)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("API port: %w", err)
	}
	postgres, err := selectOne(bindAddress, requestedPostgres, postgresStart, map[int]bool{web: true, api: true}, reserved)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("Postgres port: %w", err)
	}
	return web, api, postgres, nil
}

func Suggest(root, bindAddress string) (web, api, postgres int, err error) {
	return Select(root, bindAddress, 0, 0, 0)
}

func Available(bindAddress string, port int) bool {
	return freeOnHost(bindAddress, port)
}

func Check(root, bindAddress string, port int, exclude map[int]bool) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%d is outside 1-65535", port)
	}
	if exclude[port] {
		return fmt.Errorf("%d is already selected for this instance", port)
	}
	if reason, ok := ReservedBySiblings(root)[port]; ok {
		return fmt.Errorf("%d is reserved by another Rakazo instance at %s (%s)", port, reason.Root, reason.Role)
	}
	if !freeOnHost(bindAddress, port) {
		return fmt.Errorf("%d is already in use on %s", port, bindAddress)
	}
	return nil
}

func ReservedBySiblings(root string) map[int]Reservation {
	result := map[int]Reservation{}
	absolute, err := filepath.Abs(root)
	if err != nil {
		absolute = filepath.Clean(root)
	}
	absolute = filepath.Clean(absolute)
	parent := filepath.Dir(absolute)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(parent, entry.Name())
		if filepath.Clean(candidate) == absolute {
			continue
		}
		configPath := filepath.Join(candidate, ".manager", "instance.json")
		content, err := os.ReadFile(configPath)
		if err != nil {
			continue
		}
		var partial struct {
			WebPort      int `json:"web_port"`
			APIPort      int `json:"api_port"`
			PostgresPort int `json:"postgres_port"`
		}
		if err := json.Unmarshal(content, &partial); err != nil {
			continue
		}
		addReservation(result, partial.WebPort, candidate, "web")
		addReservation(result, partial.APIPort, candidate, "api")
		addReservation(result, partial.PostgresPort, candidate, "postgres")
	}
	return result
}

func addReservation(result map[int]Reservation, port int, root, role string) {
	if port >= 1 && port <= 65535 {
		result[port] = Reservation{Port: port, Root: root, Role: role}
	}
}

func Conflicts(root, bindAddress string, webPort, apiPort, postgresPort int) []string {
	var problems []string
	reserved := ReservedBySiblings(root)
	for _, item := range []struct {
		port int
		role string
	}{
		{webPort, "web"},
		{apiPort, "api"},
		{postgresPort, "postgres"},
	} {
		if item.port < 1 || item.port > 65535 {
			problems = append(problems, fmt.Sprintf("%s port %d is invalid", item.role, item.port))
			continue
		}
		if reason, ok := reserved[item.port]; ok {
			problems = append(problems, fmt.Sprintf("%s port %d is also reserved by %s (%s)", item.role, item.port, reason.Root, reason.Role))
		}
	}
	if webPort == apiPort && validPort(webPort) {
		problems = append(problems, "web and API share the same port")
	}
	if webPort == postgresPort && validPort(webPort) {
		problems = append(problems, "web and Postgres share the same port")
	}
	if apiPort == postgresPort && validPort(apiPort) {
		problems = append(problems, "API and Postgres share the same port")
	}
	return problems
}

func selectOne(bindAddress string, requested, start int, excluded map[int]bool, reserved map[int]Reservation) (int, error) {
	if requested != 0 {
		if err := checkCandidate(bindAddress, requested, excluded, reserved); err != nil {
			if alt, altErr := findFree(bindAddress, start, excluded, reserved); altErr == nil {
				return 0, fmt.Errorf("%w (nearest free alternative: %d)", err, alt)
			}
			return 0, err
		}
		return requested, nil
	}
	return findFree(bindAddress, start, excluded, reserved)
}

func findFree(bindAddress string, start int, excluded map[int]bool, reserved map[int]Reservation) (int, error) {
	if start < 1 {
		start = 1
	}
	for offset := 0; offset < searchWindow; offset++ {
		candidate := start + offset
		if candidate > 65535 {
			break
		}
		if err := checkCandidate(bindAddress, candidate, excluded, reserved); err != nil {
			continue
		}
		return candidate, nil
	}
	for candidate := start - 1; candidate >= 1024 && start-candidate < searchWindow; candidate-- {
		if err := checkCandidate(bindAddress, candidate, excluded, reserved); err != nil {
			continue
		}
		return candidate, nil
	}
	return 0, fmt.Errorf("no available port found near %d on %s", start, bindAddress)
}

func checkCandidate(bindAddress string, port int, excluded map[int]bool, reserved map[int]Reservation) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%d is outside 1-65535", port)
	}
	if excluded[port] {
		return fmt.Errorf("%d is already selected for this instance", port)
	}
	if reason, ok := reserved[port]; ok {
		return fmt.Errorf("%d is reserved by another Rakazo instance at %s (%s)", port, reason.Root, reason.Role)
	}
	if !freeOnHost(bindAddress, port) {
		return fmt.Errorf("%d is already in use on %s", port, bindAddress)
	}
	return nil
}

func freeOnHost(bindAddress string, port int) bool {
	addresses := listenAddressesFor(bindAddress)
	for _, address := range addresses {
		if !tryListen(address, port) {
			return false
		}
	}
	return true
}

func listenAddressesFor(bindAddress string) []string {
	switch bindAddress {
	case config.PublicBindAddress, "":
		return []string{config.PublicBindAddress}
	case config.DefaultBindAddress:
		return []string{config.DefaultBindAddress, config.PublicBindAddress}
	default:
		return []string{bindAddress}
	}
}

func tryListen(bindAddress string, port int) bool {
	listener, err := net.Listen("tcp4", net.JoinHostPort(bindAddress, strconv.Itoa(port)))
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return true
		}
		return false
	}
	return listener.Close() == nil
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}
