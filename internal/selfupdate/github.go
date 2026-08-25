package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type responseStatusError struct {
	Address string
	Code    int
	Status  string
}

func (statusError *responseStatusError) Error() string {
	return fmt.Sprintf("GET %s returned %s", statusError.Address, statusError.Status)
}

func (updater *Updater) Check(ctx context.Context, currentVersion string) (Plan, error) {
	if err := updater.validate(); err != nil {
		return Plan{}, err
	}
	assetName, err := archiveName(updater.GOOS, updater.GOARCH)
	if err != nil {
		return Plan{}, err
	}
	targetPath, err := updater.Executable()
	if err != nil {
		return Plan{}, fmt.Errorf("locate running executable: %w", err)
	}
	targetPath, err = filepath.EvalSymlinks(targetPath)
	if err != nil {
		return Plan{}, fmt.Errorf("resolve running executable %q: %w", targetPath, err)
	}

	owner, repository, _ := strings.Cut(updater.Repository, "/")
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/releases/latest",
		strings.TrimRight(updater.APIBaseURL, "/"),
		url.PathEscape(owner),
		url.PathEscape(repository),
	)
	body, err := updater.fetch(ctx, endpoint, maxReleaseResponse, "application/vnd.github+json")
	if err != nil {
		var statusError *responseStatusError
		if errors.As(err, &statusError) && statusError.Code == http.StatusNotFound {
			return Plan{}, fmt.Errorf("no published GitHub release found for %s", updater.Repository)
		}
		return Plan{}, fmt.Errorf("query latest GitHub release: %w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return Plan{}, fmt.Errorf("decode latest GitHub release: %w", err)
	}
	if strings.TrimSpace(release.TagName) == "" {
		return Plan{}, fmt.Errorf("latest GitHub release has no tag")
	}

	var assetURL, checksumsURL string
	for _, asset := range release.Assets {
		switch asset.Name {
		case assetName:
			assetURL = asset.BrowserDownloadURL
		case "checksums.txt":
			checksumsURL = asset.BrowserDownloadURL
		}
	}
	if assetURL == "" {
		return Plan{}, fmt.Errorf("release %s does not contain %s", release.TagName, assetName)
	}
	if checksumsURL == "" {
		return Plan{}, fmt.Errorf("release %s does not contain checksums.txt", release.TagName)
	}

	return Plan{
		CurrentVersion: currentVersion,
		LatestVersion:  release.TagName,
		AssetName:      assetName,
		AssetURL:       assetURL,
		ChecksumsURL:   checksumsURL,
		TargetPath:     targetPath,
		Available:      isNewer(currentVersion, release.TagName),
	}, nil
}

func (updater *Updater) validate() error {
	owner, repository, found := strings.Cut(updater.Repository, "/")
	if !found || owner == "" || repository == "" || strings.Contains(repository, "/") {
		return fmt.Errorf("invalid GitHub repository %q; expected owner/name", updater.Repository)
	}
	if updater.Client == nil {
		return fmt.Errorf("HTTP client is not configured")
	}
	if updater.Executable == nil {
		return fmt.Errorf("executable resolver is not configured")
	}
	if updater.Runner == nil {
		return fmt.Errorf("command runner is not configured")
	}
	return nil
}

func archiveName(goos, goarch string) (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("self-update is not supported on %s/%s", goos, goarch)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("self-update is not supported on %s/%s", goos, goarch)
	}
	return fmt.Sprintf("rakazo-manager_%s_%s.tar.gz", goos, goarch), nil
}

func (updater *Updater) fetch(ctx context.Context, address string, limit int64, accept string) ([]byte, error) {
	parsedAddress, err := url.Parse(address)
	if err != nil || parsedAddress.Scheme != "https" || parsedAddress.Host == "" {
		return nil, fmt.Errorf("refusing non-HTTPS download URL %q", address)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "RakazoManager-self-update")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	response, err := updater.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.Scheme != "https" {
		return nil, fmt.Errorf("refusing a download redirected away from HTTPS")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 32<<10))
		return nil, &responseStatusError{
			Address: address,
			Code:    response.StatusCode,
			Status:  response.Status,
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("GET %s exceeded the %d-byte size limit", address, limit)
	}
	return body, nil
}
