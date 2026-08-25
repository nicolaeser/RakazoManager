package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nicolaeser/RakazoManager/internal/command"
)

func (updater *Updater) Apply(ctx context.Context, plan Plan) error {
	if !plan.Available {
		return fmt.Errorf("release %s is not newer than %s", plan.LatestVersion, plan.CurrentVersion)
	}
	if plan.AssetName == "" || plan.AssetURL == "" || plan.ChecksumsURL == "" || plan.TargetPath == "" {
		return fmt.Errorf("update plan is incomplete")
	}

	manifest, err := updater.fetch(ctx, plan.ChecksumsURL, maxChecksumFile, "text/plain")
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(manifest, plan.AssetName)
	if err != nil {
		return err
	}
	archive, err := updater.fetch(ctx, plan.AssetURL, maxArchiveFile, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("download %s: %w", plan.AssetName, err)
	}
	if err := verifyChecksum(archive, expected); err != nil {
		return err
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}

	candidate, cleanup, err := writeCandidate(binary)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := installAtomically(candidate, plan.TargetPath); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("replace %s: %w", plan.TargetPath, err)
	}
	if err := updater.installPrivileged(ctx, candidate, plan.TargetPath); err != nil {
		return fmt.Errorf("replace %s with administrator privileges: %w", plan.TargetPath, err)
	}
	return nil
}

func writeCandidate(binary []byte) (string, func(), error) {
	directory, err := os.MkdirTemp("", "rakazo-manager-self-update-*")
	if err != nil {
		return "", nil, fmt.Errorf("create update workspace: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(directory)
	}
	name := filepath.Join(directory, "rakazo-manager")
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create update candidate: %w", err)
	}
	if _, err := file.Write(binary); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write update candidate: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("sync update candidate: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close update candidate: %w", err)
	}
	return name, cleanup, nil
}

func installAtomically(candidate, target string) error {
	targetInfo, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !targetInfo.Mode().IsRegular() {
		return fmt.Errorf("update target is not a regular file")
	}
	directory := filepath.Dir(target)
	staged, err := os.CreateTemp(directory, "."+filepath.Base(target)+".update-*")
	if err != nil {
		return err
	}
	stagedName := staged.Name()
	removeStaged := true
	defer func() {
		_ = staged.Close()
		if removeStaged {
			_ = os.Remove(stagedName)
		}
	}()

	source, err := os.Open(candidate)
	if err != nil {
		return err
	}
	defer source.Close()
	if _, err := staged.ReadFrom(source); err != nil {
		return err
	}
	if err := staged.Chmod(0o755); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	if err := os.Rename(stagedName, target); err != nil {
		return err
	}
	removeStaged = false

	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func (updater *Updater) installPrivileged(ctx context.Context, candidate, target string) error {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("create privileged staging name: %w", err)
	}
	staged := filepath.Join(
		filepath.Dir(target),
		"."+filepath.Base(target)+".update-"+hex.EncodeToString(suffix),
	)
	streams := command.Request{
		Stdin:  updater.In,
		Stdout: updater.Out,
		Stderr: updater.Err,
	}
	request := streams
	request.Name = "sudo"
	request.Args = []string{"install", "-m", "0755", candidate, staged}
	if _, err := updater.Runner.Run(ctx, request); err != nil {
		return err
	}

	request = streams
	request.Name = "sudo"
	request.Args = []string{"mv", "-f", staged, target}
	if _, err := updater.Runner.Run(ctx, request); err != nil {
		cleanup := streams
		cleanup.Name = "sudo"
		cleanup.Args = []string{"rm", "-f", staged}
		_, _ = updater.Runner.Run(context.Background(), cleanup)
		return err
	}
	return nil
}
