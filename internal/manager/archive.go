package manager

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxZIPArchiveBytes         int64  = 16 << 30
	maxZIPMemberBytes          uint64 = 32 << 30
	maxZIPTotalBytes           uint64 = 64 << 30
	maxZIPMembers                     = 100_000
	maxZIPMemberNameBytes             = 4 << 10
	maxZIPSymlinkTargetBytes          = 4 << 10
	maxZIPArchiveBaseNameBytes        = 255
)

type zipLimits struct {
	archiveBytes int64
	memberBytes  uint64
	totalBytes   uint64
	members      int
}

var productionZIPLimits = zipLimits{
	archiveBytes: maxZIPArchiveBytes,
	memberBytes:  maxZIPMemberBytes,
	totalBytes:   maxZIPTotalBytes,
	members:      maxZIPMembers,
}

func ValidateZIP(archivePath string) error {
	_, err := inspectZIPWithLimits(archivePath, productionZIPLimits, nil)
	return err
}

func validateZip(archivePath string) error {
	return ValidateZIP(archivePath)
}

type zipInspection struct {
	ArchivePath       string
	ArchiveBytes      int64
	Members           int
	UncompressedBytes uint64
	ContentDigest     [sha256.Size]byte
}

func inspectZIP(archivePath string, visitor func(*zip.File) error) (inspection zipInspection, resultErr error) {
	return inspectZIPWithLimits(archivePath, productionZIPLimits, visitor)
}

func inspectZIPWithLimits(archivePath string, limits zipLimits, visitor func(*zip.File) error) (inspection zipInspection, resultErr error) {
	if limits.archiveBytes <= 0 || limits.memberBytes == 0 || limits.totalBytes == 0 || limits.members <= 0 {
		return inspection, fmt.Errorf("ZIP validation limits must be positive")
	}
	info, err := os.Lstat(archivePath)
	if err != nil {
		return inspection, fmt.Errorf("inspect ZIP %s: %w", archivePath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return inspection, fmt.Errorf("ZIP archive %s is not a regular file", archivePath)
	}
	if info.Size() <= 0 {
		return inspection, fmt.Errorf("ZIP archive %s is empty", archivePath)
	}
	if info.Size() > limits.archiveBytes {
		return inspection, fmt.Errorf("ZIP archive %s exceeds the %d-byte compressed size limit", archivePath, limits.archiveBytes)
	}
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return inspection, fmt.Errorf("open ZIP %s: %w", archivePath, err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil && resultErr == nil {
			resultErr = fmt.Errorf("close ZIP %s: %w", archivePath, closeErr)
		}
	}()
	inspection = zipInspection{ArchivePath: archivePath, ArchiveBytes: info.Size(), Members: len(reader.File)}
	inspection.UncompressedBytes, inspection.ContentDigest, err = inspectOpenZIPFiles(reader.File, limits, visitor)
	if err != nil {
		return inspection, err
	}
	return inspection, nil
}

func inspectOpenZIPFiles(files []*zip.File, limits zipLimits, visitor func(*zip.File) error) (uncompressedBytes uint64, contentDigest [sha256.Size]byte, resultErr error) {
	if limits.archiveBytes <= 0 || limits.memberBytes == 0 || limits.totalBytes == 0 || limits.members <= 0 {
		return 0, contentDigest, fmt.Errorf("ZIP validation limits must be positive")
	}
	if len(files) == 0 {
		return 0, contentDigest, fmt.Errorf("ZIP archive is empty")
	}
	if len(files) > limits.members {
		return 0, contentDigest, fmt.Errorf("ZIP archive has %d members; limit is %d", len(files), limits.members)
	}
	seen := make(map[string]struct{}, len(files))
	kinds := make(map[string]os.FileMode, len(files))
	var headerTotal uint64
	for _, file := range files {
		if err := validateZIPMemberName(file.Name); err != nil {
			return 0, contentDigest, fmt.Errorf("unsafe ZIP member %q: %w", file.Name, err)
		}
		semanticName := strings.TrimSuffix(file.Name, "/")
		if _, duplicate := seen[semanticName]; duplicate {
			return 0, contentDigest, fmt.Errorf("ZIP archive contains duplicate member %q", file.Name)
		}
		seen[semanticName] = struct{}{}
		mode := file.Mode()
		if !(mode.IsRegular() || file.FileInfo().IsDir() || mode&os.ModeSymlink != 0) {
			return 0, contentDigest, fmt.Errorf("ZIP member %s has an unsupported special file type", file.Name)
		}
		kinds[semanticName] = mode
		if file.UncompressedSize64 > limits.memberBytes {
			return 0, contentDigest, fmt.Errorf("ZIP member %s exceeds the %d-byte member limit", file.Name, limits.memberBytes)
		}
		if file.CompressedSize64 > uint64(limits.archiveBytes) {
			return 0, contentDigest, fmt.Errorf("ZIP member %s has an invalid compressed size", file.Name)
		}
		if headerTotal > limits.totalBytes || file.UncompressedSize64 > limits.totalBytes-headerTotal {
			return 0, contentDigest, fmt.Errorf("ZIP archive exceeds the %d-byte total uncompressed limit", limits.totalBytes)
		}
		headerTotal += file.UncompressedSize64
	}
	for member := range kinds {
		for ancestor := path.Dir(member); ancestor != "."; ancestor = path.Dir(ancestor) {
			if ancestorMode, ok := kinds[ancestor]; ok && !ancestorMode.IsDir() {
				return 0, contentDigest, fmt.Errorf("ZIP member %q traverses non-directory ancestor %q", member, ancestor)
			}
		}
	}
	contentHash := sha256.New()
	for _, file := range files {
		writeZIPContentDigestHeader(contentHash, file)
		if visitor != nil {
			if err := visitor(file); err != nil {
				return uncompressedBytes, contentDigest, err
			}
		}
		stream, err := file.Open()
		if err != nil {
			return uncompressedBytes, contentDigest, fmt.Errorf("open ZIP member %s: %w", file.Name, err)
		}
		var symlinkTarget bytes.Buffer
		destination := io.Writer(contentHash)
		if file.Mode()&os.ModeSymlink != 0 {
			if file.UncompressedSize64 > maxZIPSymlinkTargetBytes {
				_ = stream.Close()
				return uncompressedBytes, contentDigest, fmt.Errorf("ZIP symlink member %s exceeds the %d-byte target limit", file.Name, maxZIPSymlinkTargetBytes)
			}
			destination = io.MultiWriter(&symlinkTarget, contentHash)
		}
		count, copyErr := io.Copy(destination, io.LimitReader(stream, int64(limits.memberBytes)+1))
		closeErr := stream.Close()
		if copyErr != nil {
			return uncompressedBytes, contentDigest, fmt.Errorf("verify ZIP member %s: %w", file.Name, copyErr)
		}
		if closeErr != nil {
			return uncompressedBytes, contentDigest, fmt.Errorf("close ZIP member %s: %w", file.Name, closeErr)
		}
		if count < 0 || uint64(count) > limits.memberBytes {
			return uncompressedBytes, contentDigest, fmt.Errorf("ZIP member %s exceeds the %d-byte member limit while streaming", file.Name, limits.memberBytes)
		}
		if uint64(count) != file.UncompressedSize64 {
			return uncompressedBytes, contentDigest, fmt.Errorf("ZIP member %s size mismatch: header=%d streamed=%d", file.Name, file.UncompressedSize64, count)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			if err := validateArchiveSymlink(file.Name, symlinkTarget.String()); err != nil {
				return uncompressedBytes, contentDigest, err
			}
		}
		if uncompressedBytes > limits.totalBytes || uint64(count) > limits.totalBytes-uncompressedBytes {
			return uncompressedBytes, contentDigest, fmt.Errorf("ZIP archive exceeds the %d-byte total uncompressed limit while streaming", limits.totalBytes)
		}
		uncompressedBytes += uint64(count)
	}
	copy(contentDigest[:], contentHash.Sum(nil))
	return uncompressedBytes, contentDigest, nil
}

func writeZIPContentDigestHeader(destination hash.Hash, file *zip.File) {
	var fields [24]byte
	binary.LittleEndian.PutUint64(fields[0:8], uint64(len(file.Name)))
	binary.LittleEndian.PutUint64(fields[8:16], uint64(file.Mode()))
	binary.LittleEndian.PutUint64(fields[16:24], file.UncompressedSize64)
	_, _ = destination.Write(fields[:])
	_, _ = destination.Write([]byte(file.Name))
}

func validateArchiveSymlink(memberName, target string) error {
	if target == "" || !utf8.ValidString(target) || strings.IndexFunc(target, unicode.IsControl) >= 0 ||
		strings.Contains(target, "\\") || path.IsAbs(target) {
		return fmt.Errorf("ZIP symlink member %q has an unsafe target", memberName)
	}
	member := strings.TrimSuffix(memberName, "/")
	resolved := path.Clean(path.Join(path.Dir(member), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("ZIP symlink member %q escapes the archive root", memberName)
	}
	return nil
}

func validateZIPMemberName(name string) error {
	if name == "" {
		return errors.New("name is empty")
	}
	if len(name) > maxZIPMemberNameBytes {
		return fmt.Errorf("name exceeds %d bytes", maxZIPMemberNameBytes)
	}
	if !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("name contains invalid UTF-8 or control characters")
	}
	if strings.Contains(name, "\\") {
		return errors.New("name contains a backslash")
	}
	if strings.HasPrefix(name, "/") {
		return errors.New("name is absolute")
	}
	trimmed := strings.TrimSuffix(name, "/")
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.New("name escapes the archive root")
	}
	if cleaned != trimmed {
		return errors.New("name is not normalized")
	}
	return nil
}

func validateZIPArchiveBaseName(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > maxZIPArchiveBaseNameBytes ||
		!utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 ||
		path.Base(name) != name || strings.Contains(name, "\\") {
		return fmt.Errorf("unsafe ZIP archive filename")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		return fmt.Errorf("ZIP archive filename must end in .zip")
	}
	return nil
}
