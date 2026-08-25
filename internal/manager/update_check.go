package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/registry"
)

var ErrLocalDigestUnavailable = errors.New("local registry manifest digest is unavailable")

const (
	ImageDigestSourceContainer       = "container"
	ImageDigestSourceConfiguredImage = "configured-image"
)

type ImageUpdateCheck struct {
	TrackedImage     string `json:"tracked_image"`
	PinnedImage      string `json:"pinned_image,omitempty"`
	EffectiveImage   string `json:"effective_image"`
	CurrentReference string `json:"current_reference,omitempty"`
	CurrentDigest    string `json:"current_digest,omitempty"`
	CurrentSource    string `json:"current_source,omitempty"`
	RemoteDigest     string `json:"remote_digest,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
}

func (manager *Manager) CheckImageUpdate(ctx context.Context) (ImageUpdateCheck, error) {
	return manager.CheckImageUpdateWith(ctx, registry.NewClient(nil))
}

func (manager *Manager) CheckImageUpdateWith(ctx context.Context, resolver registry.DigestResolver) (ImageUpdateCheck, error) {
	if resolver == nil {
		return ImageUpdateCheck{}, fmt.Errorf("registry digest resolver is not configured")
	}
	if err := manager.RequireInstalled(); err != nil {
		return ImageUpdateCheck{}, err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return ImageUpdateCheck{}, err
	}
	tracked, err := registry.ParseReference(cfg.Image)
	if err != nil {
		return ImageUpdateCheck{}, fmt.Errorf("parse tracked image %q: %w", cfg.Image, err)
	}
	result := ImageUpdateCheck{
		TrackedImage:   cfg.Image,
		PinnedImage:    cfg.PinnedImage,
		EffectiveImage: cfg.EffectiveImage(),
	}

	currentReference, currentDigest, source, err := manager.localManifestDigest(ctx, cfg, tracked)
	result.CurrentReference = currentReference
	result.CurrentDigest = currentDigest
	result.CurrentSource = source
	if err != nil {
		return result, err
	}

	remoteDigest, err := resolver.ManifestDigest(ctx, tracked)
	if err != nil {
		return result, fmt.Errorf("resolve remote manifest for %s: %w", cfg.Image, err)
	}
	remoteDigest = strings.ToLower(strings.TrimSpace(remoteDigest))
	if err := registry.ValidateDigest(remoteDigest); err != nil {
		return result, fmt.Errorf("registry returned an invalid manifest digest: %w", err)
	}
	result.RemoteDigest = remoteDigest
	result.UpdateAvailable = result.CurrentDigest != result.RemoteDigest
	return result, nil
}

func (manager *Manager) localManifestDigest(ctx context.Context, cfg config.Config, tracked registry.Reference) (reference, digest, source string, resultErr error) {
	containerID, err := manager.Docker.ComposeOutput(ctx, "ps", "-q", "api")
	if err != nil {
		return "", "", "", fmt.Errorf("inspect running Rakazo API: %w", err)
	}
	containerID = strings.TrimSpace(containerID)
	if containerID != "" {
		if strings.Contains(containerID, "\n") {
			return "", "", "", fmt.Errorf("multiple running Rakazo APIs were returned")
		}
		imageID, err := manager.Docker.DockerOutput(ctx, "inspect", "--format", "{{.Image}}", containerID)
		if err != nil {
			return "", "", ImageDigestSourceContainer, fmt.Errorf("inspect running Rakazo image: %w", err)
		}
		reference, digest, inspected, err := manager.repositoryDigestForImage(ctx, strings.TrimSpace(imageID), tracked)
		if err == nil {
			return reference, digest, ImageDigestSourceContainer, nil
		}
		if !inspected || !errors.Is(err, ErrLocalDigestUnavailable) {
			return "", "", ImageDigestSourceContainer, err
		}
		return "", "", ImageDigestSourceContainer, fmt.Errorf("%w for the running Rakazo API; recreate it from a registry-backed image before comparing it", ErrLocalDigestUnavailable)
	}

	effective, err := registry.ParseReference(cfg.EffectiveImage())
	if err != nil {
		return "", "", "", fmt.Errorf("parse effective image %q: %w", cfg.EffectiveImage(), err)
	}
	reference, digest, inspected, err := manager.repositoryDigestForImage(ctx, cfg.EffectiveImage(), tracked)
	if err == nil {
		return reference, digest, ImageDigestSourceConfiguredImage, nil
	}
	if !inspected {
		return "", "", ImageDigestSourceConfiguredImage, fmt.Errorf("%w for %s: %w", ErrLocalDigestUnavailable, cfg.EffectiveImage(), err)
	}
	if !errors.Is(err, ErrLocalDigestUnavailable) {
		return "", "", "", err
	}
	if inspected && effective.Digest != "" && effective.SameRepository(tracked) {
		return effective.String(), effective.Digest, ImageDigestSourceConfiguredImage, nil
	}
	return "", "", ImageDigestSourceConfiguredImage, fmt.Errorf("%w for %s; pull or start the configured image before comparing it", ErrLocalDigestUnavailable, cfg.EffectiveImage())
}

func (manager *Manager) repositoryDigestForImage(ctx context.Context, image string, tracked registry.Reference) (reference, digest string, inspected bool, resultErr error) {
	output, err := manager.Docker.DockerOutput(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", image)
	if err != nil {
		return "", "", false, fmt.Errorf("inspect local image %s: %w", image, err)
	}
	var references []string
	if err := json.Unmarshal([]byte(output), &references); err != nil {
		return "", "", true, fmt.Errorf("parse local image registry digests: %w", err)
	}
	var selectedReference, selectedDigest string
	for _, value := range references {
		candidate, err := registry.ParseReference(value)
		if err != nil || candidate.Digest == "" || !candidate.SameRepository(tracked) {
			continue
		}
		if selectedDigest != "" && selectedDigest != candidate.Digest {
			return "", "", true, fmt.Errorf("local image has multiple manifest digests for %s", tracked.Repository)
		}
		selectedReference = value
		selectedDigest = candidate.Digest
	}
	if selectedDigest == "" {
		return "", "", true, fmt.Errorf("%w for repository %s", ErrLocalDigestUnavailable, tracked.Repository)
	}
	return selectedReference, selectedDigest, true, nil
}
