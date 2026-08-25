package manager

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicolaeser/RakazoManager/internal/compose"
	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/ports"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
	"github.com/nicolaeser/RakazoManager/internal/stack"
	"github.com/nicolaeser/RakazoManager/internal/state"
)

const (
	maxImportManifestBytes = 1 << 20
	maxImportMetadataBytes = 16 << 20
)

type ImportInstanceOptions struct {
	Pull   bool        `json:"pull"`
	Start  bool        `json:"start"`
	DryRun bool        `json:"dry_run"`
	Patch  ConfigPatch `json:"patch"`
}

type ImportInstancePlan struct {
	Archive           string                 `json:"archive"`
	Target            string                 `json:"target"`
	Manifest          InstanceExportManifest `json:"manifest"`
	Config            config.Config          `json:"config"`
	EmbeddedBackup    string                 `json:"embedded_backup"`
	IncludesWorkspace bool                   `json:"includes_workspace"`
	Actions           []string               `json:"actions"`
}

type ImportInstanceResult struct {
	Plan     ImportInstancePlan `json:"plan"`
	Applied  bool               `json:"applied"`
	Started  bool               `json:"started"`
	Restored bool               `json:"restored"`
}

type inspectedInstanceExport struct {
	plan          ImportInstancePlan
	memberName    string
	secrets       secrets.Values
	patch         ConfigPatch
	archiveDigest [sha256.Size]byte
}

func (manager *Manager) PlanImportInstance(archivePath string, options ImportInstanceOptions) (ImportInstancePlan, error) {
	inspected, err := manager.planImportInstance(archivePath, options)
	if err != nil {
		return ImportInstancePlan{}, err
	}
	return inspected.plan, nil
}

func (manager *Manager) planImportInstance(archivePath string, options ImportInstanceOptions) (inspectedInstanceExport, error) {
	if err := ensureImportTargetAvailable(manager.Paths.Root); err != nil {
		return inspectedInstanceExport{}, err
	}
	manager.progress("Validating and inspecting the instance export archive")
	inspected, err := inspectInstanceExport(archivePath, manager.Paths, options.Patch)
	if err != nil {
		return inspectedInstanceExport{}, err
	}
	if err := validateImportedConfig(manager.Paths.Root, inspected.plan.Config); err != nil {
		return inspectedInstanceExport{}, err
	}
	inspected.plan.Actions = []string{
		"extract only validated manager metadata, the embedded Rakazo backup, and the optional workspace into a private staging directory",
		"ignore the archived generated Compose file and regenerate docker-compose.yml from validated metadata",
		"publish the staged instance into the new or empty target",
	}
	if options.Pull {
		inspected.plan.Actions = append(inspected.plan.Actions, "pull the configured Rakazo image")
	}
	if options.Start {
		inspected.plan.Actions = append(inspected.plan.Actions,
			"start Rakazo and verify its persistent bind mounts",
			"import the embedded Rakazo backup with --force",
			"restart Rakazo and verify bind mounts and API health",
		)
	} else {
		inspected.plan.Actions = append(inspected.plan.Actions,
			"leave the embedded Rakazo backup staged; complete recovery by starting this instance and restoring the staged backup",
		)
	}
	return inspected, nil
}

func (manager *Manager) ImportInstance(ctx context.Context, archivePath string, options ImportInstanceOptions) (result ImportInstanceResult, operationErr error) {
	inspected, err := manager.planImportInstance(archivePath, options)
	result.Plan = inspected.plan
	if err != nil || options.DryRun {
		return result, err
	}
	manager.progress("Extracting validated recovery files into private staging and regenerating Compose")
	stagingRoot, err := prepareImportedInstance(inspected, manager.Paths)
	if err != nil {
		return result, err
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(stagingRoot)
		}
	}()
	if err := ensureImportTargetAvailable(manager.Paths.Root); err != nil {
		return result, err
	}
	manager.progress("Publishing the validated staged instance into the target folder")
	if err := publishImportedInstance(stagingRoot, manager.Paths.Root); err != nil {
		return result, err
	}
	removeStaging = false
	result.Applied = true

	lock, err := manager.operationLock("import-instance")
	if err != nil {
		return result, err
	}
	defer releaseLock(lock, &operationErr)

	if options.Pull || options.Start {
		manager.progress("Checking Docker and validating the regenerated Compose configuration")
		if err := manager.Docker.CheckDaemon(ctx); err != nil {
			return result, err
		}
		if err := manager.Docker.ValidateCompose(ctx); err != nil {
			return result, fmt.Errorf("validate regenerated Compose configuration: %w", err)
		}
	}
	if options.Pull {
		manager.progress("Pulling the configured Rakazo image")
		if err := manager.Docker.Compose(ctx, false, "pull"); err != nil {
			return result, fmt.Errorf("pull imported instance image: %w", err)
		}
	}
	if options.Start {
		manager.progress("Starting the recovered Rakazo stack")
		upArgs := []string{"up", "-d", "--wait", "--wait-timeout", "300"}
		if !options.Pull {
			upArgs = append(upArgs, "--pull", "never")
		}
		if err := manager.Docker.Compose(ctx, false, upArgs...); err != nil {
			return result, fmt.Errorf("start imported instance: %w", err)
		}
		result.Started = true
		if err := manager.verifyContainerBinds(ctx); err != nil {
			return result, fmt.Errorf("verify imported instance bind mounts: %w", err)
		}
		manager.progress("Restoring the embedded backup")
		if err := manager.restoreHostBackup(ctx, result.Plan.EmbeddedBackup); err != nil {
			return result, fmt.Errorf("restore embedded Rakazo backup: %w", err)
		}
		if err := manager.Docker.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300", "--pull", "never"); err != nil {
			return result, fmt.Errorf("restart imported Rakazo instance: %w", err)
		}
		if err := manager.verifyContainerBinds(ctx); err != nil {
			return result, fmt.Errorf("verify bind mounts after embedded backup import: %w", err)
		}
		if _, err := manager.waitForAPI(ctx, apiReadyTimeout); err != nil {
			return result, fmt.Errorf("verify API after embedded backup import: %w", err)
		}
		result.Restored = true
	}
	managerState, stateErr := manager.StateStore.Load()
	if stateErr == nil {
		managerState.LastBackup = filepath.Base(result.Plan.EmbeddedBackup)
		managerState.LastOperation = "import-instance"
		_ = manager.StateStore.Save(managerState)
	}
	_ = manager.StateStore.Log("import-instance", fmt.Sprintf("archive=%s started=%t restored=%t result=success", filepath.Base(result.Plan.Archive), result.Started, result.Restored))
	return result, nil
}

func inspectInstanceExport(archivePath string, target stack.Paths, patch ConfigPatch) (inspectedInstanceExport, error) {
	absolute, err := filepath.Abs(archivePath)
	if err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("resolve instance export: %w", err)
	}
	absolute = filepath.Clean(absolute)
	pathInfo, err := os.Lstat(absolute)
	if err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("inspect instance export: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Size() <= 0 || pathInfo.Size() > maxZIPArchiveBytes {
		return inspectedInstanceExport{}, fmt.Errorf("instance export is not a bounded regular file")
	}
	archiveFile, err := os.Open(absolute)
	if err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("open instance export: %w", err)
	}
	defer archiveFile.Close()
	openedInfo, err := archiveFile.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		return inspectedInstanceExport{}, fmt.Errorf("instance export changed while it was being opened")
	}
	reader, err := zip.NewReader(archiveFile, openedInfo.Size())
	if err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("open instance export ZIP: %w", err)
	}
	_, archiveDigest, err := inspectOpenZIPFiles(reader.File, productionZIPLimits, nil)
	if err != nil {
		return inspectedInstanceExport{}, err
	}
	files := make(map[string]*zip.File, len(reader.File))
	semanticNames := make(map[string]bool, len(reader.File))
	for _, file := range reader.File {
		semantic := strings.TrimSuffix(file.Name, "/")
		if semanticNames[semantic] {
			return inspectedInstanceExport{}, fmt.Errorf("instance export contains conflicting member %q", file.Name)
		}
		semanticNames[semantic] = true
		files[file.Name] = file
	}

	manifestFile, ok := files["manifest.json"]
	if !ok || !zipRegular(manifestFile) {
		return inspectedInstanceExport{}, fmt.Errorf("instance export is missing regular manifest.json")
	}
	manifestBytes, err := readZIPMember(manifestFile, maxImportManifestBytes)
	if err != nil {
		return inspectedInstanceExport{}, err
	}
	var manifest InstanceExportManifest
	if err := decodeStrictJSON(manifestBytes, &manifest); err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("parse instance export manifest: %w", err)
	}
	if err := validateInstanceExportManifest(manifest); err != nil {
		return inspectedInstanceExport{}, err
	}
	embeddedMember := "rakazo/" + manifest.RakazoBackup

	for _, required := range []string{".manager/instance.json", ".manager/secrets.env", "generated/docker-compose.yml", embeddedMember} {
		file, exists := files[required]
		if !exists || !zipRegular(file) {
			return inspectedInstanceExport{}, fmt.Errorf("instance export is missing regular %s", required)
		}
	}
	if err := validateInstanceExportMembers(reader.File, manifest, embeddedMember); err != nil {
		return inspectedInstanceExport{}, err
	}
	if err := validateEmbeddedRakazoBackup(archiveFile, files[embeddedMember]); err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("validate embedded Rakazo backup: %w", err)
	}

	configBytes, err := readZIPMember(files[".manager/instance.json"], maxImportMetadataBytes)
	if err != nil {
		return inspectedInstanceExport{}, err
	}
	var archivedConfig config.Config
	if err := decodeStrictJSON(configBytes, &archivedConfig); err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("parse archived instance metadata: %w", err)
	}
	if archivedConfig.BindAddress == "" {
		archivedConfig.BindAddress = config.DefaultBindAddress
	}
	if err := archivedConfig.Validate(); err != nil {
		return inspectedInstanceExport{}, fmt.Errorf("validate archived instance metadata: %w", err)
	}
	if archivedConfig.Name != manifest.InstanceName || archivedConfig.Image != manifest.TrackedImage {
		return inspectedInstanceExport{}, fmt.Errorf("instance export manifest does not match archived instance metadata")
	}
	if manifest.RunningImage != "" {
		if !safeImportedCredentialText(manifest.RunningImage) {
			return inspectedInstanceExport{}, fmt.Errorf("instance export manifest contains an unsafe running image reference")
		}
		runningImage := archivedConfig
		runningImage.PinnedImage = manifest.RunningImage
		if err := runningImage.Validate(); err != nil {
			return inspectedInstanceExport{}, fmt.Errorf("instance export manifest contains an invalid running image reference")
		}
	}
	configPlan, err := planConfigPatch(archivedConfig, patch)
	if err != nil {
		return inspectedInstanceExport{}, err
	}

	secretBytes, err := readZIPMember(files[".manager/secrets.env"], maxImportMetadataBytes)
	if err != nil {
		return inspectedInstanceExport{}, err
	}
	secretValues, err := parseImportedSecrets(secretBytes)
	if err != nil {
		return inspectedInstanceExport{}, err
	}
	for _, key := range []string{secrets.BetterAuthSecret, secrets.EncryptionKey, secrets.SupervisorToken, secrets.PostgresPassword} {
		if secretValues[key] == "" {
			return inspectedInstanceExport{}, fmt.Errorf("archived secrets are missing %s", key)
		}
	}
	if stateFile := files[".manager/state.json"]; stateFile != nil {
		if !zipRegular(stateFile) {
			return inspectedInstanceExport{}, fmt.Errorf("archived manager state is not a regular file")
		}
		stateBytes, err := readZIPMember(stateFile, maxImportMetadataBytes)
		if err != nil {
			return inspectedInstanceExport{}, err
		}
		var managerState state.State
		if err := decodeStrictJSON(stateBytes, &managerState); err != nil || managerState.SchemaVersion != state.SchemaVersion {
			return inspectedInstanceExport{}, fmt.Errorf("archived manager state is invalid or unsupported")
		}
		if !safeImportedCredentialText(managerState.PreviousImage) || !safeImportedCredentialText(managerState.LastBackup) || !safeImportedCredentialText(managerState.LastOperation) {
			return inspectedInstanceExport{}, fmt.Errorf("archived manager state contains invalid or control characters")
		}
		if managerState.PreviousImage != "" {
			stateImage := archivedConfig
			stateImage.PinnedImage = managerState.PreviousImage
			if err := stateImage.Validate(); err != nil {
				return inspectedInstanceExport{}, fmt.Errorf("archived manager state contains an invalid rollback image")
			}
		}
		if managerState.LastBackup != "" {
			if err := validateZIPArchiveBaseName(managerState.LastBackup); err != nil {
				return inspectedInstanceExport{}, fmt.Errorf("archived manager state contains an unsafe backup name")
			}
		}
	}
	if operationsFile := files[".manager/operations.log"]; operationsFile != nil {
		if !zipRegular(operationsFile) {
			return inspectedInstanceExport{}, fmt.Errorf("archived manager operation log is not a regular file")
		}
		operationsBytes, err := readZIPMember(operationsFile, maxImportMetadataBytes)
		if err != nil {
			return inspectedInstanceExport{}, err
		}
		if !safeImportedOperationLog(string(operationsBytes)) {
			return inspectedInstanceExport{}, fmt.Errorf("archived manager operation log contains invalid or terminal control characters")
		}
	}

	return inspectedInstanceExport{
		plan: ImportInstancePlan{
			Archive:           absolute,
			Target:            target.Root,
			Manifest:          manifest,
			Config:            configPlan.After,
			EmbeddedBackup:    filepath.Join(target.Backups, manifest.RakazoBackup),
			IncludesWorkspace: manifest.IncludesWorkspace,
		},
		memberName:    embeddedMember,
		secrets:       secretValues,
		patch:         patch,
		archiveDigest: archiveDigest,
	}, nil
}

func validateEmbeddedRakazoBackup(outer *os.File, embedded *zip.File) error {
	if embedded == nil || embedded.Method != zip.Store || embedded.CompressedSize64 != embedded.UncompressedSize64 {
		return fmt.Errorf("embedded backup must be stored without outer ZIP compression")
	}
	if embedded.UncompressedSize64 == 0 || embedded.UncompressedSize64 > uint64(maxZIPArchiveBytes) {
		return fmt.Errorf("embedded backup size is outside the supported limit")
	}
	offset, err := embedded.DataOffset()
	if err != nil {
		return fmt.Errorf("locate embedded backup: %w", err)
	}
	section := io.NewSectionReader(outer, offset, int64(embedded.UncompressedSize64))
	reader, err := zip.NewReader(section, int64(embedded.UncompressedSize64))
	if err != nil {
		return fmt.Errorf("open embedded backup ZIP: %w", err)
	}
	if _, _, err := inspectOpenZIPFiles(reader.File, productionZIPLimits, nil); err != nil {
		return err
	}
	return nil
}

func validateInstanceExportManifest(manifest InstanceExportManifest) error {
	if manifest.SchemaVersion != instanceExportSchema {
		return fmt.Errorf("unsupported instance export schema %d", manifest.SchemaVersion)
	}
	if manifest.CreatedAt.IsZero() {
		return fmt.Errorf("instance export manifest has no creation time")
	}
	if strings.TrimSpace(manifest.InstanceName) == "" || strings.TrimSpace(manifest.TrackedImage) == "" {
		return fmt.Errorf("instance export manifest is missing identity or image")
	}
	if !manifest.ContainsSecrets {
		return fmt.Errorf("instance export does not declare the required credentials")
	}
	if err := validateZIPArchiveBaseName(manifest.RakazoBackup); err != nil {
		return fmt.Errorf("instance export manifest has an unsafe embedded backup name")
	}
	return nil
}

func validateInstanceExportMembers(files []*zip.File, manifest InstanceExportManifest, embeddedMember string) error {
	var symlinks []string
	workspaceKinds := make(map[string]os.FileMode)
	workspaceMembers := 0
	workspaceRoot := false
	for _, file := range files {
		name := file.Name
		allowed := name == "manifest.json" || name == "RECOVERY.txt" ||
			name == ".manager/instance.json" || name == ".manager/secrets.env" ||
			name == ".manager/state.json" || name == ".manager/operations.log" ||
			name == "generated/docker-compose.yml" || name == embeddedMember ||
			name == "workspace/" || strings.HasPrefix(name, "workspace/")
		if !allowed {
			return fmt.Errorf("instance export contains unsupported member %q", name)
		}
		if name == "workspace/" || strings.HasPrefix(name, "workspace/") {
			workspaceMembers++
			mode := file.Mode()
			if name == "workspace/" && !mode.IsDir() {
				return fmt.Errorf("instance export workspace/ root must be a directory")
			}
			if name == "workspace/" {
				workspaceRoot = true
			}
			if !(mode.IsRegular() || mode.IsDir() || mode&os.ModeSymlink != 0) {
				return fmt.Errorf("workspace member %q has unsupported file type", name)
			}
			if mode&os.ModeSymlink != 0 {
				targetBytes, err := readZIPMember(file, maxZIPSymlinkTargetBytes)
				if err != nil {
					return err
				}
				if err := validateWorkspaceSymlink(name, string(targetBytes)); err != nil {
					return err
				}
				symlinks = append(symlinks, strings.TrimSuffix(name, "/"))
			}
			workspaceKinds[strings.TrimSuffix(name, "/")] = mode
		} else if !zipRegular(file) {
			return fmt.Errorf("instance export member %q must be a regular file", name)
		}
	}
	if !manifest.IncludesWorkspace && workspaceMembers > 0 {
		return fmt.Errorf("instance export contains workspace members but manifest says workspace is absent")
	}
	if manifest.IncludesWorkspace && !workspaceRoot {
		return fmt.Errorf("instance export manifest declares a workspace but workspace/ directory is missing")
	}
	for _, file := range files {
		name := strings.TrimSuffix(file.Name, "/")
		for _, link := range symlinks {
			if name != link && strings.HasPrefix(name, link+"/") {
				return fmt.Errorf("instance export member %q traverses workspace symlink %q", file.Name, link)
			}
		}
	}
	for member := range workspaceKinds {
		for ancestor := path.Dir(member); ancestor != "." && ancestor != "workspace"; ancestor = path.Dir(ancestor) {
			if ancestorMode, ok := workspaceKinds[ancestor]; ok && !ancestorMode.IsDir() {
				return fmt.Errorf("instance export workspace member %q traverses non-directory ancestor %q", member, ancestor)
			}
		}
	}
	return nil
}

func validateWorkspaceSymlink(memberName, target string) error {
	if target == "" || !utf8.ValidString(target) || strings.IndexFunc(target, unicode.IsControl) >= 0 ||
		strings.Contains(target, "\\") || path.IsAbs(target) {
		return fmt.Errorf("workspace symlink %q has an unsafe target", memberName)
	}
	linkPath := strings.TrimPrefix(strings.TrimSuffix(memberName, "/"), "workspace/")
	resolved := path.Clean(path.Join(path.Dir(linkPath), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("workspace symlink %q escapes workspace", memberName)
	}
	return nil
}

func validateImportedConfig(root string, cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := validateUniqueInstanceName(root, cfg.Name); err != nil {
		return fmt.Errorf("%w; supply a name override", err)
	}
	if err := ports.Check(root, cfg.BindAddress, cfg.WebPort, map[int]bool{cfg.APIPort: true, cfg.PostgresPort: true}); err != nil {
		return fmt.Errorf("web port: %w", err)
	}
	if err := ports.Check(root, cfg.BindAddress, cfg.APIPort, map[int]bool{cfg.WebPort: true, cfg.PostgresPort: true}); err != nil {
		return fmt.Errorf("API port: %w", err)
	}
	if err := ports.Check(root, cfg.BindAddress, cfg.PostgresPort, map[int]bool{cfg.WebPort: true, cfg.APIPort: true}); err != nil {
		return fmt.Errorf("Postgres port: %w", err)
	}
	return nil
}

func ensureImportTargetAvailable(root string) error {
	if filepath.Clean(root) == "." || filepath.Clean(root) == string(filepath.Separator) {
		return fmt.Errorf("refuse unsafe import target %s", root)
	}
	parentInfo, err := os.Stat(filepath.Dir(root))
	if err != nil {
		return fmt.Errorf("inspect import target parent: %w", err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("import target parent is not a directory")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect import target: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("import target must be a new or empty real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect import target contents: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("import target %s is not empty", root)
	}
	return nil
}

func prepareImportedInstance(inspected inspectedInstanceExport, finalPaths stack.Paths) (string, error) {
	stagingRoot, err := os.MkdirTemp(filepath.Dir(finalPaths.Root), ".rakazo-import-*")
	if err != nil {
		return "", fmt.Errorf("create private import staging directory: %w", err)
	}
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", fmt.Errorf("protect import staging directory: %w", err)
	}
	stagingPaths, err := stack.NewPaths(stagingRoot)
	if err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", err
	}
	stagedArchive := filepath.Join(stagingRoot, ".validated-instance-export.zip")
	if err := copyImportArchive(inspected.plan.Archive, stagedArchive); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", err
	}
	stagedInspection, err := inspectInstanceExport(stagedArchive, finalPaths, inspected.patch)
	if err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", fmt.Errorf("revalidate privately staged instance export: %w", err)
	}
	if stagedInspection.plan.Manifest != inspected.plan.Manifest || stagedInspection.plan.Config != inspected.plan.Config ||
		stagedInspection.memberName != inspected.memberName || !maps.Equal(stagedInspection.secrets, inspected.secrets) ||
		stagedInspection.archiveDigest != inspected.archiveDigest {
		_ = os.RemoveAll(stagingRoot)
		return "", fmt.Errorf("instance export changed after planning; retry with a stable archive")
	}
	if err := extractInstanceExport(stagedInspection, stagingPaths); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", err
	}
	if err := os.Remove(stagedArchive); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", fmt.Errorf("remove private staged export after extraction: %w", err)
	}
	if err := (config.Store{Paths: stagingPaths}).Save(inspected.plan.Config); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", err
	}
	secretStore := secrets.Store{Paths: stagingPaths}
	if err := secretStore.Save(inspected.secrets); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", err
	}
	if err := (compose.Generator{Paths: stagingPaths}).Prepare(inspected.plan.Config, inspected.secrets, true); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", fmt.Errorf("regenerate imported Compose file: %w", err)
	}
	if err := ValidateZIP(filepath.Join(stagingPaths.Backups, inspected.plan.Manifest.RakazoBackup)); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return "", fmt.Errorf("validate embedded Rakazo backup: %w", err)
	}
	return stagingRoot, nil
}

func copyImportArchive(source, destination string) error {
	before, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect instance export before staging: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return fmt.Errorf("instance export is not a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open instance export for private staging: %w", err)
	}
	defer input.Close()
	after, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened instance export: %w", err)
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return fmt.Errorf("instance export changed while it was being opened")
	}
	if after.Size() <= 0 || after.Size() > maxZIPArchiveBytes {
		return fmt.Errorf("instance export size is outside the supported limit")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private staged instance export: %w", err)
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, maxZIPArchiveBytes+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("copy instance export into private staging: %v", errors.Join(copyErr, syncErr, closeErr))
	}
	if written != after.Size() || written > maxZIPArchiveBytes {
		_ = os.Remove(destination)
		return fmt.Errorf("instance export changed size while being staged")
	}
	return nil
}

func extractInstanceExport(inspected inspectedInstanceExport, target stack.Paths) error {
	reader, err := zip.OpenReader(inspected.plan.Archive)
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := validateCurrentInstanceExport(reader.File, inspected); err != nil {
		return fmt.Errorf("validate instance export immediately before extraction: %w", err)
	}
	for _, directory := range []struct {
		path string
		mode os.FileMode
	}{
		{target.Manager, 0o700}, {target.Workspace, 0o755}, {target.Backups, 0o700},
	} {
		if err := os.MkdirAll(directory.path, directory.mode); err != nil {
			return fmt.Errorf("create import directory: %w", err)
		}
	}
	var symlinks []*zip.File
	var extractedBytes uint64
	for _, file := range reader.File {
		var destination string
		var mode os.FileMode
		switch file.Name {
		case ".manager/instance.json":
			destination, mode = target.Config, 0o600
		case ".manager/secrets.env":
			destination, mode = target.Secrets, 0o600
		case ".manager/state.json":
			destination, mode = target.State, 0o600
		case ".manager/operations.log":
			destination, mode = target.OperationsLog, 0o600
		case inspected.memberName:
			destination, mode = filepath.Join(target.Backups, inspected.plan.Manifest.RakazoBackup), 0o600
		default:
			if file.Name == "workspace/" || strings.HasPrefix(file.Name, "workspace/") {
				relative := strings.TrimPrefix(strings.TrimSuffix(file.Name, "/"), "workspace/")
				destination, err = workspaceImportDestination(target.Workspace, relative)
				if err != nil {
					return err
				}
				if file.Mode()&os.ModeSymlink != 0 {
					symlinks = append(symlinks, file)
					continue
				}
				mode = safeWorkspaceMode(file.Mode())
			} else {
				continue
			}
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, mode); err != nil {
				return err
			}
			continue
		}
		written, err := extractRegularZIPMember(file, destination, mode)
		if err != nil {
			return err
		}
		if extractedBytes > maxZIPTotalBytes || written > maxZIPTotalBytes-extractedBytes {
			return fmt.Errorf("instance export exceeds the %d-byte extraction limit", maxZIPTotalBytes)
		}
		extractedBytes += written
	}
	for _, file := range symlinks {
		relative := strings.TrimPrefix(strings.TrimSuffix(file.Name, "/"), "workspace/")
		destination, err := workspaceImportDestination(target.Workspace, relative)
		if err != nil {
			return err
		}
		targetBytes, err := readZIPMember(file, maxZIPSymlinkTargetBytes)
		if err != nil {
			return err
		}
		if err := validateWorkspaceSymlink(file.Name, string(targetBytes)); err != nil {
			return err
		}
		if extractedBytes > maxZIPTotalBytes || uint64(len(targetBytes)) > maxZIPTotalBytes-extractedBytes {
			return fmt.Errorf("instance export exceeds the %d-byte extraction limit", maxZIPTotalBytes)
		}
		extractedBytes += uint64(len(targetBytes))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(string(targetBytes), destination); err != nil {
			return fmt.Errorf("create workspace symlink %s: %w", file.Name, err)
		}
	}
	return nil
}

func validateCurrentInstanceExport(files []*zip.File, inspected inspectedInstanceExport) error {
	_, currentDigest, err := inspectOpenZIPFiles(files, productionZIPLimits, nil)
	if err != nil {
		return err
	}
	if currentDigest != inspected.archiveDigest {
		return fmt.Errorf("instance export changed after validation; retry with a stable archive")
	}
	byName := make(map[string]*zip.File, len(files))
	for _, file := range files {
		byName[file.Name] = file
	}
	for _, required := range []string{".manager/instance.json", ".manager/secrets.env", "generated/docker-compose.yml", inspected.memberName} {
		if !zipRegular(byName[required]) {
			return fmt.Errorf("instance export is missing regular %s", required)
		}
	}
	return validateInstanceExportMembers(files, inspected.plan.Manifest, inspected.memberName)
}

func workspaceImportDestination(workspace, relative string) (string, error) {
	destination := filepath.Join(workspace, filepath.FromSlash(relative))
	contained, err := filepath.Rel(filepath.Clean(workspace), filepath.Clean(destination))
	if err != nil {
		return "", fmt.Errorf("resolve workspace import member %q: %w", relative, err)
	}
	if contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) || filepath.IsAbs(contained) {
		return "", fmt.Errorf("workspace import member %q escapes the staging workspace", relative)
	}
	return destination, nil
}

func extractRegularZIPMember(file *zip.File, destination string, mode os.FileMode) (uint64, error) {
	if !zipRegular(file) {
		return 0, fmt.Errorf("refuse non-regular import member %s", file.Name)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return 0, err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, fmt.Errorf("create imported file %s: %w", destination, err)
	}
	stream, err := file.Open()
	if err != nil {
		_ = output.Close()
		return 0, err
	}
	written, copyErr := io.Copy(output, io.LimitReader(stream, int64(maxZIPMemberBytes)+1))
	streamErr := stream.Close()
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || streamErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		return 0, fmt.Errorf("extract import member %s: %v", file.Name, errors.Join(copyErr, streamErr, syncErr, closeErr))
	}
	if written < 0 || uint64(written) > maxZIPMemberBytes || uint64(written) != file.UncompressedSize64 {
		_ = os.Remove(destination)
		return 0, fmt.Errorf("extract import member %s: streamed size does not match its validated header", file.Name)
	}
	return uint64(written), nil
}

func publishImportedInstance(stagingRoot, targetRoot string) error {
	originalMode := os.FileMode(0o755)
	targetExisted := false
	if info, err := os.Lstat(targetRoot); err == nil {
		targetExisted = true
		originalMode = info.Mode().Perm()
		entries, readErr := os.ReadDir(targetRoot)
		if readErr != nil || len(entries) != 0 {
			return fmt.Errorf("import target changed while staging")
		}
		if err := os.Remove(targetRoot); err != nil {
			return fmt.Errorf("prepare empty import target: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stagingRoot, targetRoot); err != nil {
		if targetExisted {
			_ = os.Mkdir(targetRoot, originalMode)
		}
		return fmt.Errorf("publish imported instance: %w", err)
	}
	return nil
}

func readZIPMember(file *zip.File, limit int64) ([]byte, error) {
	if file.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("ZIP member %s exceeds the %d-byte import metadata limit", file.Name, limit)
	}
	stream, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open ZIP member %s: %w", file.Name, err)
	}
	content, readErr := io.ReadAll(io.LimitReader(stream, limit+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("read ZIP member %s: %v", file.Name, errors.Join(readErr, closeErr))
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("ZIP member %s exceeds the %d-byte import metadata limit", file.Name, limit)
	}
	return content, nil
}

func decodeStrictJSON(content []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func parseImportedSecrets(content []byte) (secrets.Values, error) {
	values := secrets.Values{}
	allowed := map[string]bool{
		secrets.PostgresPassword: true,
		secrets.DatabaseURL:      true,
		secrets.BetterAuthSecret: true,
		secrets.EncryptionKey:    true,
		secrets.SupervisorToken:  true,
		secrets.OpenRouterAPIKey: true,
		secrets.ComposioAPIKey:   true,
	}
	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || !allowed[key] || !safeImportedCredentialText(key) || !safeImportedCredentialText(value) {
			return nil, fmt.Errorf("invalid archived credential line")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("duplicate archived credential %s", key)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read archived credentials: %w", err)
	}
	return values, nil
}

func safeImportedCredentialText(value string) bool {
	return utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func safeImportedOperationLog(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character) && character != '\n' && character != '\t'
	}) < 0
}

func zipRegular(file *zip.File) bool {
	return file != nil && file.Mode().IsRegular() && !file.FileInfo().IsDir()
}

func safeWorkspaceMode(mode os.FileMode) os.FileMode {
	permissions := mode.Perm()
	if mode.IsDir() {
		if permissions == 0 {
			return 0o755
		}
		return permissions
	}
	if permissions == 0 {
		return 0o644
	}
	return permissions
}
