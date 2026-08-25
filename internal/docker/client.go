package docker

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/fsutil"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

const PrimaryService = "api"

var AppServices = []string{"api", "worker", "web", "supervisor"}

type Client struct {
	Paths  stack.Paths
	Runner command.Runner
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
}

func New(paths stack.Paths, runner command.Runner, in io.Reader, out, errOut io.Writer) Client {
	return Client{Paths: paths, Runner: runner, In: in, Out: out, Err: errOut}
}

func (client Client) CheckCLI(ctx context.Context) error {
	if _, err := client.capture(ctx, "docker", "--version"); err != nil {
		return err
	}
	if _, err := client.capture(ctx, "docker", "compose", "version"); err != nil {
		return fmt.Errorf("Docker Compose v2 is required: %w", err)
	}
	return nil
}

func (client Client) CheckDaemon(ctx context.Context) error {
	if err := client.CheckCLI(ctx); err != nil {
		return err
	}
	if _, err := client.capture(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return fmt.Errorf("Docker daemon is unavailable: %w", err)
	}
	return nil
}

func (client Client) Compose(ctx context.Context, interactive bool, args ...string) error {
	requestArgs := client.composeArguments(args...)
	request := command.Request{
		Name:      "docker",
		Args:      requestArgs,
		Directory: client.Paths.Root,
		Stdout:    client.Out,
		Stderr:    client.Err,
		Capture:   false,
	}
	if interactive {
		request.Stdin = client.In
	}
	_, err := client.Runner.Run(ctx, request)
	return err
}

func (client Client) ComposeOutput(ctx context.Context, args ...string) (string, error) {
	result, err := client.Runner.Run(ctx, command.Request{
		Name:      "docker",
		Args:      client.composeArguments(args...),
		Directory: client.Paths.Root,
		Capture:   true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (client Client) Docker(ctx context.Context, interactive bool, args ...string) error {
	request := command.Request{
		Name:      "docker",
		Args:      args,
		Directory: client.Paths.Root,
		Stdout:    client.Out,
		Stderr:    client.Err,
		Capture:   false,
	}
	if interactive {
		request.Stdin = client.In
	}
	_, err := client.Runner.Run(ctx, request)
	return err
}

func (client Client) DockerOutput(ctx context.Context, args ...string) (string, error) {
	return client.capture(ctx, "docker", args...)
}

func (client Client) ServiceRunning(ctx context.Context) bool {
	running, err := client.ServiceRunningStatus(ctx)
	return err == nil && running
}

func (client Client) ServiceRunningStatus(ctx context.Context) (bool, error) {
	output, err := client.ComposeOutput(ctx, "ps", "--status", "running", "--services")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == PrimaryService {
			return true, nil
		}
	}
	return false, nil
}

func (client Client) ServiceExists(ctx context.Context) (bool, error) {
	output, err := client.ComposeOutput(ctx, "ps", "-a", "-q", PrimaryService)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) != "", nil
}

func (client Client) EnsureRunning(ctx context.Context) error {
	if client.ServiceRunning(ctx) {
		return nil
	}
	return client.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300")
}

func (client Client) ValidateCompose(ctx context.Context) error {
	return client.Compose(ctx, false, "config", "--quiet")
}

func (client Client) Exec(ctx context.Context, interactive bool, service string, serviceArgs ...string) error {
	args := []string{"exec"}
	if !interactive {
		args = append(args, "-T")
	}
	args = append(args, service)
	args = append(args, serviceArgs...)
	return client.Compose(ctx, interactive, args...)
}

func (client Client) Shell(ctx context.Context, shellCommand ...string) error {
	if len(shellCommand) == 0 {
		shellCommand = []string{"bash"}
	}
	args := client.composeArguments(append([]string{"exec", PrimaryService}, shellCommand...)...)
	request := command.Request{
		Name:           "docker",
		Args:           args,
		Directory:      client.Paths.Root,
		Stdin:          client.In,
		Stdout:         client.Out,
		Stderr:         client.Err,
		Capture:        false,
		InheritSignals: true,
	}
	_, err := client.Runner.Run(ctx, request)
	return err
}

func (client Client) ExecOutput(ctx context.Context, service string, serviceArgs ...string) (string, error) {
	args := []string{"exec", "-T", service}
	args = append(args, serviceArgs...)
	return client.ComposeOutput(ctx, args...)
}

func (client Client) composeFile() string {
	if fsutil.FileExists(client.Paths.Compose) {
		return client.Paths.Compose
	}
	if fsutil.FileExists(client.Paths.LegacyCompose()) {
		return client.Paths.LegacyCompose()
	}
	return client.Paths.Compose
}

func (client Client) composeArguments(args ...string) []string {
	base := []string{
		"compose",
		"--project-directory", client.Paths.Root,
		"-f", client.composeFile(),
	}
	if fsutil.FileExists(client.Paths.ComposeOverride()) {
		base = append(base, "-f", client.Paths.ComposeOverride())
	}
	return append(base, args...)
}

func (client Client) capture(ctx context.Context, name string, args ...string) (string, error) {
	result, err := client.Runner.Run(ctx, command.Request{
		Name:      name,
		Args:      args,
		Directory: client.Paths.Root,
		Capture:   true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Stdout), nil
}

func (client Client) IsInstalled() bool {
	composePresent := fsutil.FileExists(client.Paths.Compose) || fsutil.FileExists(client.Paths.LegacyCompose())
	return composePresent && fsutil.FileExists(client.Paths.Config)
}

func (client Client) RedactedCompose(ctx context.Context, sensitiveValues ...string) (string, error) {
	output, err := client.ComposeOutput(ctx, "config")
	if err != nil {
		return "", err
	}
	for _, value := range sensitiveValues {
		if value != "" {
			output = strings.ReplaceAll(output, value, "<redacted>")
		}
	}
	return output, nil
}

func DefaultIO() (io.Reader, io.Writer, io.Writer) {
	return os.Stdin, os.Stdout, os.Stderr
}
