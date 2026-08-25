package manager

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/config"
)

func (manager *Manager) Backup(ctx context.Context, label string) (archive string, operationErr error) {
	if err := manager.ensureRunning(ctx); err != nil {
		return "", err
	}
	lock, err := manager.operationLock("backup")
	if err != nil {
		return "", err
	}
	defer releaseLock(lock, &operationErr)
	return manager.backupUnlocked(ctx, label)
}

func (manager *Manager) backupUnlocked(ctx context.Context, label string) (string, error) {
	label = SanitizeLabel(label)
	if label == "" {
		label = "manual"
	}
	filename := fmt.Sprintf("rakazo-%s-%s.zip", label, time.Now().UTC().Format("20060102T150405.000Z"))
	hostPath := filepath.Join(manager.Paths.Backups, filename)
	if err := manager.createHostBackup(ctx, hostPath); err != nil {
		return "", fmt.Errorf("create Rakazo backup: %w", err)
	}
	if err := os.Chmod(hostPath, 0o600); err != nil {
		return "", fmt.Errorf("protect new backup: %w", err)
	}
	if err := validateZip(hostPath); err != nil {
		return "", fmt.Errorf("validate new backup: %w", err)
	}
	managerState, err := manager.StateStore.Load()
	if err != nil {
		return "", err
	}
	managerState.LastBackup = filename
	managerState.LastOperation = "backup"
	if err := manager.StateStore.Save(managerState); err != nil {
		return "", err
	}
	_ = manager.StateStore.Log("backup", "file="+filename+" result=success")
	return hostPath, nil
}

func (manager *Manager) ListBackups() ([]string, error) {
	if err := manager.RequireInstalled(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(manager.Paths.Backups)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".zip") || strings.HasSuffix(entry.Name(), ".tar.gz") {
			files = append(files, entry.Name())
		}
	}
	return files, nil
}

func (manager *Manager) Restore(ctx context.Context, requested string) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	lock, err := manager.operationLock("restore")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)

	backupsPath := manager.Paths.Backups
	archivePath, archiveName, err := stageRestoreArchive(requested, backupsPath)
	if err != nil {
		return err
	}
	if err := validateZip(archivePath); err != nil {
		return err
	}
	if err := manager.ensureRunning(ctx); err != nil {
		return err
	}

	if _, err := manager.backupUnlocked(ctx, "pre-restore"); err != nil {
		return fmt.Errorf("create pre-restore safety backup: %w", err)
	}
	if err := manager.restoreHostBackup(ctx, archivePath); err != nil {
		return fmt.Errorf("restore backup: %w", err)
	}
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300"); err != nil {
		return fmt.Errorf("restart after restore: %w", err)
	}
	managerState, err := manager.StateStore.Load()
	if err == nil {
		managerState.LastOperation = "restore"
		_ = manager.StateStore.Save(managerState)
	}
	_ = manager.StateStore.Log("restore", "file="+archiveName+" result=success")
	return nil
}

func (manager *Manager) Update(ctx context.Context) (operationErr error) {
	if err := manager.ensureRunning(ctx); err != nil {
		return err
	}
	lock, err := manager.operationLock("update")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)

	manager.progress("Checking host data directories and container bind mounts")
	hadState, err := manager.preUpdateSnapshot()
	if err != nil {
		return err
	}
	if err := manager.verifyContainerBinds(ctx); err != nil {
		return fmt.Errorf("pre-update mount check failed (refusing to update with unsafe mounts): %w", err)
	}

	manager.progress("Creating pre-update safety backup")
	if _, err := manager.backupUnlocked(ctx, "pre-update"); err != nil {
		return fmt.Errorf("create pre-update backup: %w", err)
	}
	previousImage, err := manager.currentImageReference(ctx)
	if err != nil {
		return err
	}
	manager.progress("Recording previous image for automatic rollback: %s", previousImage)
	managerState, err := manager.StateStore.Load()
	if err != nil {
		return err
	}
	managerState.PreviousImage = previousImage
	managerState.LastOperation = "update-started"
	if err := manager.StateStore.Save(managerState); err != nil {
		return err
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	cfg.PinnedImage = ""
	if err := manager.saveAndPrepare(cfg); err != nil {
		return err
	}
	manager.progress("Pulling the newest GHCR app image (host data is not touched)")
	if err := manager.Docker.Compose(ctx, false, "pull", "api", "worker", "web", "supervisor"); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		if rollbackErr != nil {
			return fmt.Errorf("pull updated image: %w; restoring the previous image also failed: %v", err, rollbackErr)
		}
		return fmt.Errorf("pull updated image: %w; the previous image was restored", err)
	}
	manager.progress("Recreating application containers with the new image (preserving bind mounts)")
	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300", "--force-recreate", "--remove-orphans", "api", "worker", "web", "supervisor"); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("recreate updated containers", err, rollbackErr)
	}
	if !manager.Docker.ServiceRunning(ctx) {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("updated container is not running", nil, rollbackErr)
	}
	manager.progress("Verifying bind mounts and host application data after recreate")
	if err := manager.verifyContainerBinds(ctx); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("post-update mount check failed", err, rollbackErr)
	}
	if err := manager.assertDataSurvived(hadState); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("post-update data check failed", err, rollbackErr)
	}
	manager.progress("Waiting for API health")
	if _, err := manager.waitForAPI(ctx, apiReadyTimeout); err != nil {
		rollbackErr := manager.pinAndRecreate(ctx, previousImage)
		return joinedUpdateError("updated API health check failed", err, rollbackErr)
	}
	managerState.LastOperation = "update"
	if err := manager.StateStore.Save(managerState); err != nil {
		return err
	}
	_ = manager.StateStore.Log("update", "previous_image="+previousImage+" result=success")
	manager.progress("Update finished; host data/, workspace/, and backups/ were left in place")
	return nil
}

func (manager *Manager) Rollback(ctx context.Context) (operationErr error) {
	if err := manager.RequireInstalled(); err != nil {
		return err
	}
	if err := manager.Docker.CheckDaemon(ctx); err != nil {
		return err
	}
	managerState, err := manager.StateStore.Load()
	if err != nil {
		return err
	}
	if managerState.PreviousImage == "" {
		return fmt.Errorf("no previous image has been recorded")
	}
	lock, err := manager.operationLock("rollback")
	if err != nil {
		return err
	}
	defer releaseLock(lock, &operationErr)
	if manager.Docker.ServiceRunning(ctx) {
		if _, err := manager.backupUnlocked(ctx, "pre-image-rollback"); err != nil {
			return fmt.Errorf("create pre-rollback backup: %w", err)
		}
	} else {
		fmt.Fprintln(manager.Err, "warning: current container is not running; proceeding without a pre-rollback backup")
	}
	if err := manager.pinAndRecreate(ctx, managerState.PreviousImage); err != nil {
		return err
	}
	managerState.LastOperation = "rollback"
	if err := manager.StateStore.Save(managerState); err != nil {
		return err
	}
	_ = manager.StateStore.Log("rollback", "image="+managerState.PreviousImage+" result=success")
	return nil
}

func (manager *Manager) currentImageReference(ctx context.Context) (string, error) {
	containerID, err := manager.Docker.ComposeOutput(ctx, "ps", "-q", "api")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(containerID) == "" {
		return "", fmt.Errorf("Rakazo API container is not running")
	}
	imageID, err := manager.Docker.DockerOutput(ctx, "inspect", "--format", "{{.Image}}", strings.TrimSpace(containerID))
	if err != nil {
		return "", err
	}
	reference, err := manager.Docker.DockerOutput(ctx, "image", "inspect", "--format", "{{if .RepoDigests}}{{index .RepoDigests 0}}{{else}}{{.Id}}{{end}}", strings.TrimSpace(imageID))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(reference) == "" {
		return "", fmt.Errorf("could not resolve running image digest")
	}
	return strings.TrimSpace(reference), nil
}

func (manager *Manager) pinAndRecreate(ctx context.Context, image string) error {
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	previousPin := cfg.PinnedImage
	cfg.PinnedImage = image
	if err := manager.saveAndPrepare(cfg); err != nil {
		return err
	}

	if err := manager.Docker.Compose(ctx, false, "up", "-d", "--wait", "--wait-timeout", "300", "--force-recreate", "api", "worker", "web", "supervisor"); err != nil {
		cfg.PinnedImage = previousPin
		_ = manager.saveAndPrepare(cfg)
		return fmt.Errorf("recreate rollback image: %w", err)
	}
	if err := manager.verifyContainerBinds(ctx); err != nil {
		return fmt.Errorf("rollback image started but bind mounts are wrong: %w", err)
	}
	if _, err := manager.waitForAPI(ctx, apiReadyTimeout); err != nil {
		return fmt.Errorf("rollback image started but API health verification failed: %w", err)
	}
	return nil
}

func (manager *Manager) SetBindAddress(ctx context.Context, public bool) error {
	want := config.DefaultBindAddress
	if public {
		want = config.PublicBindAddress
	}
	current, err := manager.ConfigStore.Load()
	if err != nil {
		return err
	}
	if current.BindAddress == want {
		manager.progress("Bind address already %s", want)
		return nil
	}
	manager.progress("Updating bind address %s → %s", current.BindAddress, want)
	_, err = manager.ApplyConfigPatch(ctx, ConfigPatch{BindAddress: &want}, false)
	return err
}

func stageRestoreArchive(requested, backupsPath string) (string, string, error) {
	if strings.TrimSpace(requested) == "" {
		return "", "", fmt.Errorf("backup path is required")
	}
	if !filepath.IsAbs(requested) {
		requested = filepath.Join(backupsPath, requested)
	}
	absolute, err := filepath.Abs(requested)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", "", fmt.Errorf("inspect backup: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("backup is not a regular file")
	}
	if !strings.HasSuffix(strings.ToLower(absolute), ".zip") {
		return "", "", fmt.Errorf("full restore requires a Rakazo .zip backup")
	}

	sourceInspection, err := inspectZIP(absolute, nil)
	if err != nil {
		return "", "", err
	}
	name := filepath.Base(absolute)
	if err := validateZIPArchiveBaseName(name); err != nil {
		return "", "", fmt.Errorf("unsafe backup filename %q: %w", name, err)
	}
	destination := filepath.Join(backupsPath, name)
	sourceDirectory := filepath.Clean(filepath.Dir(absolute))
	if sourceDirectory != filepath.Clean(backupsPath) {
		if _, err := os.Lstat(destination); err == nil {
			return "", "", fmt.Errorf("backup %s already exists in %s", name, backupsPath)
		} else if !os.IsNotExist(err) {
			return "", "", err
		}
		if err := copyRestoreArchiveExclusive(absolute, destination, info, sourceInspection.ContentDigest); err != nil {
			return "", "", err
		}
	}
	return destination, name, nil
}

func copyRestoreArchiveExclusive(source, destination string, sourceInfo os.FileInfo, expectedDigest [32]byte) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open restore archive: %w", err)
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened restore archive: %w", err)
	}
	if !os.SameFile(sourceInfo, openedInfo) || !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("restore archive changed while it was being opened")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("backup %s already exists in %s", filepath.Base(destination), filepath.Dir(destination))
		}
		return fmt.Errorf("create staged restore archive: %w", err)
	}
	removeDestination := true
	defer func() {
		_ = output.Close()
		if removeDestination {
			_ = os.Remove(destination)
		}
	}()
	written, copyErr := io.Copy(output, io.LimitReader(input, maxZIPArchiveBytes+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("stage restore archive: %v", errors.Join(copyErr, syncErr, closeErr))
	}
	if written != openedInfo.Size() || written > maxZIPArchiveBytes {
		return fmt.Errorf("restore archive changed size while it was staged")
	}
	stagedInspection, err := inspectZIP(destination, nil)
	if err != nil {
		return fmt.Errorf("validate staged restore archive: %w", err)
	}
	if stagedInspection.ContentDigest != expectedDigest {
		return fmt.Errorf("restore archive changed while it was staged")
	}
	removeDestination = false
	return nil
}

func joinedUpdateError(stage string, updateErr, rollbackErr error) error {
	message := stage
	if updateErr != nil {
		message += ": " + updateErr.Error()
	}
	if rollbackErr == nil {
		return fmt.Errorf("%s; automatically rolled back and pinned the previous image", message)
	}
	return fmt.Errorf("%s; automatic rollback also failed: %v", message, rollbackErr)
}

func (manager *Manager) createHostBackup(ctx context.Context, destination string) error {
	if err := os.MkdirAll(manager.Paths.Backups, 0o700); err != nil {
		return err
	}
	dump, err := manager.Docker.ComposeOutput(ctx, "exec", "-T", "postgres", "pg_dump", "-U", "rakazo", "-d", "rakazo", "-Fc")
	if err != nil {
		return fmt.Errorf("dump Postgres: %w", err)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create backup archive: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	writer := zip.NewWriter(file)
	dumpFile, err := writer.Create("postgres.dump")
	if err != nil {
		return err
	}
	if _, err := dumpFile.Write([]byte(dump)); err != nil {
		return err
	}
	if info, statErr := os.Stat(manager.Paths.App); statErr == nil && info.IsDir() {
		err = filepath.WalkDir(manager.Paths.App, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(manager.Paths.App, path)
			if err != nil {
				return err
			}
			header, err := zip.FileInfoHeader(mustFileInfo(entry, path))
			if err != nil {
				return err
			}
			header.Name = filepath.ToSlash(filepath.Join("app", relative))
			header.Method = zip.Deflate
			target, err := writer.CreateHeader(header)
			if err != nil {
				return err
			}
			source, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(target, source)
			closeErr := source.Close()
			return errors.Join(copyErr, closeErr)
		})
		if err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	remove = false
	return nil
}

func (manager *Manager) restoreHostBackup(ctx context.Context, archivePath string) error {
	staging, err := os.MkdirTemp(manager.Paths.Backups, "restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	var dumpPath string
	for _, file := range reader.File {
		name := filepath.Clean(file.Name)
		if strings.HasPrefix(name, "..") {
			return fmt.Errorf("unsafe backup member %s", file.Name)
		}
		target := filepath.Join(staging, name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := extractZipFile(file, target); err != nil {
			return err
		}
		if name == "postgres.dump" {
			dumpPath = target
		}
	}
	if dumpPath == "" {
		return fmt.Errorf("backup is missing postgres.dump")
	}
	if err := manager.Docker.Compose(ctx, false, "stop", "api", "worker", "web"); err != nil {
		return err
	}
	hostedDump := filepath.Join(manager.Paths.Backups, ".restore-postgres.dump")
	dumpBytes, err := os.ReadFile(dumpPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(hostedDump, dumpBytes, 0o600); err != nil {
		return err
	}
	defer os.Remove(hostedDump)
	if err := manager.Docker.Exec(ctx, false, "postgres", "pg_restore", "-U", "rakazo", "-d", "rakazo", "--clean", "--if-exists", "/backups/.restore-postgres.dump"); err != nil {
		return fmt.Errorf("restore Postgres dump: %w", err)
	}
	appStaging := filepath.Join(staging, "app")
	if info, err := os.Stat(appStaging); err == nil && info.IsDir() {
		if err := os.RemoveAll(manager.Paths.App); err != nil {
			return err
		}
		if err := os.Rename(appStaging, manager.Paths.App); err != nil {
			return fmt.Errorf("replace application data: %w", err)
		}
	}
	return nil
}

func extractZipFile(file *zip.File, destination string) error {
	input, err := file.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func mustFileInfo(entry fs.DirEntry, path string) os.FileInfo {
	info, err := entry.Info()
	if err != nil {
		info, _ = os.Stat(path)
	}
	return info
}
