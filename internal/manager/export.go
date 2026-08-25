package manager

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/config"
	"github.com/nicolaeser/RakazoManager/internal/secrets"
	"github.com/nicolaeser/RakazoManager/internal/stack"
)

const instanceExportSchema = 1

type InstanceExportManifest struct {
	SchemaVersion     int       `json:"schema_version"`
	CreatedAt         time.Time `json:"created_at"`
	InstanceName      string    `json:"instance_name"`
	TrackedImage      string    `json:"tracked_image"`
	RunningImage      string    `json:"running_image"`
	RakazoBackup      string    `json:"rakazo_backup"`
	IncludesWorkspace bool      `json:"includes_workspace"`
	ContainsSecrets   bool      `json:"contains_secrets"`
}

func (manager *Manager) ExportInstance(ctx context.Context, includeWorkspace bool) (archive string, operationErr error) {
	if err := manager.ensureRunning(ctx); err != nil {
		return "", err
	}
	lock, err := manager.operationLock("export-instance")
	if err != nil {
		return "", err
	}
	defer releaseLock(lock, &operationErr)
	if err := validateExportCredentialContract(manager.Paths); err != nil {
		return "", err
	}

	embeddedBackup, err := manager.backupUnlocked(ctx, "instance-export")
	if err != nil {
		return "", fmt.Errorf("create embedded Rakazo backup: %w", err)
	}
	cfg, err := manager.ConfigStore.Load()
	if err != nil {
		return "", err
	}
	runningImage, err := manager.currentImageReference(ctx)
	if err != nil {
		return "", err
	}
	filename := fmt.Sprintf("rakazo-instance-%s-%s.zip", SanitizeLabel(cfg.Name), time.Now().UTC().Format("20060102T150405.000Z"))
	destination := filepath.Join(manager.Paths.Backups, filename)
	manifest := InstanceExportManifest{
		SchemaVersion:     instanceExportSchema,
		CreatedAt:         time.Now().UTC(),
		InstanceName:      cfg.Name,
		TrackedImage:      cfg.Image,
		RunningImage:      runningImage,
		RakazoBackup:      filepath.Base(embeddedBackup),
		IncludesWorkspace: includeWorkspace,
		ContainsSecrets:   true,
	}
	if err := writeInstanceExport(manager.Paths, manifest, embeddedBackup, destination, includeWorkspace); err != nil {
		return "", err
	}
	if err := validateZip(destination); err != nil {
		return "", fmt.Errorf("validate instance export: %w", err)
	}

	if err := os.Remove(embeddedBackup); err != nil {
		fmt.Fprintf(manager.Err, "warning: instance export succeeded but temporary backup could not be removed: %v\n", err)
	}
	managerState, err := manager.StateStore.Load()
	if err == nil {
		managerState.LastBackup = filename
		managerState.LastOperation = "export-instance"
		_ = manager.StateStore.Save(managerState)
	}
	_ = manager.StateStore.Log("export-instance", fmt.Sprintf("file=%s workspace=%t contains_secrets=true result=success", filename, includeWorkspace))
	return destination, nil
}

func writeInstanceExport(paths stack.Paths, manifest InstanceExportManifest, embeddedBackup, destination string, includeWorkspace bool) (resultErr error) {
	if err := validateExportCredentialContract(paths); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(paths.Backups, ".rakazo-instance-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary instance export: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary instance export: %w", err)
	}

	archive := zip.NewWriter(temporary)
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode instance export manifest: %w", err)
	}
	manifestJSON = append(manifestJSON, '\n')
	if err := addBytesToZip(archive, "manifest.json", manifestJSON, 0o600); err != nil {
		return err
	}
	recoveryGuide := `Rakazo Manager disaster-recovery export

WARNING: This archive contains instance secrets in .manager/secrets.env.
Store it like a password vault export.

Contents:
- rakazo/ contains a complete Rakazo backup for rakazo-manager restore.
- workspace/ contains the host workspace when requested.
- .manager/ contains identity, credentials, state, and operation history.
- generated/docker-compose.yml is included for auditing only. Regenerate Compose on
  the recovery host because its absolute bind-mount paths belong to the old host.
`
	if err := addBytesToZip(archive, "RECOVERY.txt", []byte(recoveryGuide), 0o600); err != nil {
		return err
	}

	for _, item := range []struct {
		source   string
		name     string
		required bool
	}{
		{paths.Config, ".manager/instance.json", true},
		{paths.Secrets, ".manager/secrets.env", true},
		{paths.State, ".manager/state.json", false},
		{paths.OperationsLog, ".manager/operations.log", false},
		{paths.Compose, "generated/docker-compose.yml", true},
		{embeddedBackup, "rakazo/" + filepath.Base(embeddedBackup), true},
	} {
		if err := addFileToZip(archive, item.source, item.name); err != nil {
			if !item.required && os.IsNotExist(unwrapFileError(err)) {
				continue
			}
			return err
		}
	}
	if includeWorkspace {
		if err := addWorkspaceToZip(archive, paths.Workspace); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("finalize instance export ZIP: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync instance export: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close instance export: %w", err)
	}
	if err := validateZip(temporaryPath); err != nil {
		return fmt.Errorf("validate temporary instance export: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("publish instance export: %w", err)
	}
	removeTemporary = false
	return nil
}

func addBytesToZip(archive *zip.Writer, name string, content []byte, mode fs.FileMode) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	header.SetMode(mode)
	header.SetModTime(time.Now().UTC())
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create export member %s: %w", name, err)
	}
	if _, err := writer.Write(content); err != nil {
		return fmt.Errorf("write export member %s: %w", name, err)
	}
	return nil
}

func addFileToZip(archive *zip.Writer, source, name string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect export source %s: %w", source, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("export source %s is not a regular file", source)
	}
	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open export source %s: %w", source, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened export source %s: %w", source, err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("export source %s changed while it was being opened", source)
	}
	header, err := zip.FileInfoHeader(openedInfo)
	if err != nil {
		return fmt.Errorf("create ZIP header for %s: %w", source, err)
	}
	header.Name = name
	header.Method = zip.Deflate
	if strings.HasSuffix(strings.ToLower(name), ".zip") {
		header.Method = zip.Store
	}
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create export member %s: %w", name, err)
	}
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("copy export source %s: %w", source, err)
	}
	return nil
}

func addWorkspaceToZip(archive *zip.Writer, workspace string) error {
	if err := addBytesToZip(archive, "workspace/", nil, fs.ModeDir|0o755); err != nil {
		return err
	}
	return filepath.WalkDir(workspace, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == workspace {
			return nil
		}
		relative, err := filepath.Rel(workspace, path)
		if err != nil {
			return err
		}
		if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("workspace entry escapes root: %s", path)
		}
		name := "workspace/" + filepath.ToSlash(relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = name
		switch {
		case info.IsDir():
			header.Name += "/"
			header.Method = zip.Store
			_, err = archive.CreateHeader(header)
			return err
		case info.Mode()&os.ModeSymlink != 0:
			header.Method = zip.Store
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			if err := validateWorkspaceSymlink(name, target); err != nil {
				return fmt.Errorf("refuse unsafe workspace symlink during export: %w", err)
			}
			writer, createErr := archive.CreateHeader(header)
			if createErr != nil {
				return createErr
			}
			_, err = io.WriteString(writer, target)
			return err
		case info.Mode().IsRegular():
			header.Method = zip.Deflate
			file, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			openedInfo, statErr := file.Stat()
			if statErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
				_ = file.Close()
				if statErr != nil {
					return statErr
				}
				return fmt.Errorf("workspace file changed while it was being opened: %s", path)
			}
			writer, createErr := archive.CreateHeader(header)
			if createErr != nil {
				_ = file.Close()
				return createErr
			}
			_, copyErr := io.Copy(writer, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		default:
			return fmt.Errorf("workspace contains unsupported special file: %s", path)
		}
	})
}

func validateExportCredentialContract(paths stack.Paths) error {
	configInfo, err := os.Lstat(paths.Config)
	if err != nil {
		return fmt.Errorf("inspect export metadata: %w", err)
	}
	if !configInfo.Mode().IsRegular() {
		return fmt.Errorf("export metadata must be a regular file")
	}
	if _, err := (config.Store{Paths: paths}).Load(); err != nil {
		return err
	}
	info, err := os.Lstat(paths.Secrets)
	if err != nil {
		return fmt.Errorf("inspect export credentials: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxImportMetadataBytes {
		return fmt.Errorf("export credentials must be a bounded regular file")
	}
	file, err := os.Open(paths.Secrets)
	if err != nil {
		return fmt.Errorf("open export credentials: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened export credentials: %w", err)
	}
	if !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
		return fmt.Errorf("export credentials changed while they were being opened")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxImportMetadataBytes+1))
	if err != nil {
		return fmt.Errorf("read bounded export credentials: %w", err)
	}
	if len(content) > maxImportMetadataBytes {
		return fmt.Errorf("export credentials exceed the supported size")
	}
	values, err := parseImportedSecrets(content)
	if err != nil {
		return fmt.Errorf("credentials cannot be represented by the instance export contract: %w", err)
	}
	for _, key := range []string{secrets.BetterAuthSecret, secrets.EncryptionKey, secrets.SupervisorToken, secrets.PostgresPassword} {
		if values[key] == "" {
			return fmt.Errorf("credentials cannot be represented by the instance export contract: missing %s", key)
		}
	}
	return nil
}

func unwrapFileError(err error) error {
	for err != nil {
		if pathError, ok := err.(*os.PathError); ok {
			return pathError.Err
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = unwrapper.Unwrap()
	}
	return nil
}
