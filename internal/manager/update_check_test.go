package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/command"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/registry"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

type digestResolverStub struct {
	digest    string
	err       error
	reference registry.Reference
}

func (resolver *digestResolverStub) ManifestDigest(_ context.Context, reference registry.Reference) (string, error) {
	resolver.reference = reference
	return resolver.digest, resolver.err
}

type scriptedRunner struct {
	t         *testing.T
	responses []scriptedResponse
	requests  []command.Request
}

type scriptedResponse struct {
	name   string
	args   []string
	stdout string
	stderr string
	err    error
}

func (runner *scriptedRunner) Run(_ context.Context, request command.Request) (command.Result, error) {
	runner.t.Helper()
	runner.requests = append(runner.requests, request)
	if len(runner.responses) == 0 {
		runner.t.Fatalf("unexpected command: %s %v", request.Name, request.Args)
	}
	response := runner.responses[0]
	runner.responses = runner.responses[1:]
	if request.Name != response.name || !reflect.DeepEqual(request.Args, response.args) {
		runner.t.Fatalf("command = %s %v, want %s %v", request.Name, request.Args, response.name, response.args)
	}
	return command.Result{Stdout: response.stdout, Stderr: response.stderr}, response.err
}

func TestCheckImageUpdateUsesRunningContainerDigestWithoutMutation(t *testing.T) {
	manager, paths := installedTestManager(t, nil, config.DefaultImage, "")
	localDigest := "sha256:" + strings.Repeat("a", 64)
	remoteDigest := "sha256:" + strings.Repeat("b", 64)
	runner := &scriptedRunner{t: t, responses: []scriptedResponse{
		{name: "docker", args: composeArgs(paths, "ps", "-q", "api"), stdout: "container-id\n"},
		{name: "docker", args: []string{"inspect", "--format", "{{.Image}}", "container-id"}, stdout: "image-id\n"},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{json .RepoDigests}}", "image-id"}, stdout: fmt.Sprintf(`["ghcr.io/elie222/rakazo/app@%s"]`, localDigest)},
	}}
	manager.Docker.Runner = runner
	resolver := &digestResolverStub{digest: remoteDigest}

	result, err := manager.CheckImageUpdateWith(context.Background(), resolver)
	if err != nil {
		t.Fatalf("CheckImageUpdateWith: %v", err)
	}
	if !result.UpdateAvailable || result.CurrentDigest != localDigest || result.RemoteDigest != remoteDigest || result.CurrentSource != ImageDigestSourceContainer {
		t.Fatalf("unexpected result: %#v", result)
	}
	if resolver.reference.Repository != "elie222/rakazo/app" || resolver.reference.Tag != "edge" {
		t.Fatalf("unexpected resolver reference: %#v", resolver.reference)
	}
	assertNoMutatingDockerRequest(t, runner.requests)
}

func TestCheckImageUpdateFallsBackToStoppedConfiguredImage(t *testing.T) {
	pinned := "ghcr.io/elie222/rakazo/app:stable"
	manager, paths := installedTestManager(t, nil, config.DefaultImage, pinned)
	digest := "sha256:" + strings.Repeat("c", 64)
	runner := &scriptedRunner{t: t, responses: []scriptedResponse{
		{name: "docker", args: composeArgs(paths, "ps", "-q", "api")},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{json .RepoDigests}}", pinned}, stdout: fmt.Sprintf(`["ghcr.io/elie222/rakazo/app@%s"]`, digest)},
	}}
	manager.Docker.Runner = runner
	resolver := &digestResolverStub{digest: digest}

	result, err := manager.CheckImageUpdateWith(context.Background(), resolver)
	if err != nil {
		t.Fatalf("CheckImageUpdateWith: %v", err)
	}
	if result.UpdateAvailable || result.CurrentSource != ImageDigestSourceConfiguredImage || result.EffectiveImage != pinned || result.PinnedImage != pinned {
		t.Fatalf("unexpected result: %#v", result)
	}
	assertNoMutatingDockerRequest(t, runner.requests)
}

func TestCheckImageUpdateDoesNotSubstituteCachedTagForRunningContainer(t *testing.T) {
	manager, paths := installedTestManager(t, nil, config.DefaultImage, "")
	remoteDigest := "sha256:" + strings.Repeat("d", 64)
	runner := &scriptedRunner{t: t, responses: []scriptedResponse{
		{name: "docker", args: composeArgs(paths, "ps", "-q", "api"), stdout: "container-id\n"},
		{name: "docker", args: []string{"inspect", "--format", "{{.Image}}", "container-id"}, stdout: "running-image-id\n"},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{json .RepoDigests}}", "running-image-id"}, stdout: "null"},
	}}
	manager.Docker.Runner = runner
	resolver := &digestResolverStub{digest: remoteDigest}

	result, err := manager.CheckImageUpdateWith(context.Background(), resolver)
	if !errors.Is(err, ErrLocalDigestUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if result.CurrentSource != ImageDigestSourceContainer || result.UpdateAvailable || result.RemoteDigest != "" {
		t.Fatalf("incomparable running image produced a result: %#v", result)
	}
	if resolver.reference.Repository != "" {
		t.Fatal("remote resolver ran without a comparable running digest")
	}
}

func TestCheckImageUpdateReturnsIndeterminateWithoutLocalDigest(t *testing.T) {
	manager, paths := installedTestManager(t, nil, config.DefaultImage, "")
	runner := &scriptedRunner{t: t, responses: []scriptedResponse{
		{name: "docker", args: composeArgs(paths, "ps", "-q", "api")},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{json .RepoDigests}}", config.DefaultImage}, stdout: "null"},
	}}
	manager.Docker.Runner = runner
	resolver := &digestResolverStub{digest: "sha256:" + strings.Repeat("d", 64)}

	result, err := manager.CheckImageUpdateWith(context.Background(), resolver)
	if !errors.Is(err, ErrLocalDigestUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if result.UpdateAvailable || result.RemoteDigest != "" {
		t.Fatalf("indeterminate result claimed availability: %#v", result)
	}
	if resolver.reference.Repository != "" {
		t.Fatalf("remote resolver should not run without a local digest")
	}
}

func TestCheckImageUpdateReturnsIndeterminateWhenConfiguredImageIsMissing(t *testing.T) {
	pinned := "ghcr.io/elie222/rakazo/app@sha256:" + strings.Repeat("e", 64)
	manager, paths := installedTestManager(t, nil, config.DefaultImage, pinned)
	runner := &scriptedRunner{t: t, responses: []scriptedResponse{
		{name: "docker", args: composeArgs(paths, "ps", "-q", "api")},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{json .RepoDigests}}", pinned}, err: errors.New("image not found")},
	}}
	manager.Docker.Runner = runner
	resolver := &digestResolverStub{digest: "sha256:" + strings.Repeat("f", 64)}

	result, err := manager.CheckImageUpdateWith(context.Background(), resolver)
	if !errors.Is(err, ErrLocalDigestUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if result.UpdateAvailable || result.CurrentDigest != "" || result.RemoteDigest != "" {
		t.Fatalf("missing local image claimed a comparable digest: %#v", result)
	}
	if resolver.reference.Repository != "" {
		t.Fatalf("remote resolver should not run without a local image")
	}
}

func TestCheckImageUpdatePreservesConfiguredImageInspectionCancellation(t *testing.T) {
	manager, paths := installedTestManager(t, nil, config.DefaultImage, "")
	runner := &scriptedRunner{t: t, responses: []scriptedResponse{
		{name: "docker", args: composeArgs(paths, "ps", "-q", "api")},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{json .RepoDigests}}", config.DefaultImage}, err: context.Canceled},
	}}
	manager.Docker.Runner = runner

	_, err := manager.CheckImageUpdateWith(context.Background(), &digestResolverStub{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrLocalDigestUnavailable) {
		t.Fatalf("error does not preserve cancellation and indeterminate state: %v", err)
	}
}

func installedTestManager(t *testing.T, runner command.Runner, image, pinned string) (*Manager, stack.Paths) {
	t.Helper()
	paths, err := stack.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Compose, []byte("services:\n  api:\n    image: test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(paths.Root, "test-instance", image, 9119, 12000, 15433)
	cfg.PinnedImage = pinned
	if err := (config.Store{Paths: paths}).Save(cfg); err != nil {
		t.Fatal(err)
	}
	if runner == nil {
		runner = &scriptedRunner{t: t}
	}
	manager := New(paths, runner, strings.NewReader(""), io.Discard, io.Discard)
	return manager, paths
}

func composeArgs(paths stack.Paths, args ...string) []string {
	base := []string{"compose", "--project-directory", paths.Root, "-f", paths.Compose}
	return append(base, args...)
}

func assertNoMutatingDockerRequest(t *testing.T, requests []command.Request) {
	t.Helper()
	for _, request := range requests {
		joined := strings.Join(request.Args, " ")
		for _, forbidden := range []string{" pull ", " up ", " create ", " rm ", " down "} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("update check ran mutating Docker command: %s %s", request.Name, joined)
			}
		}
	}
}

func testExecutable(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "rakazo-manager")
	if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
