package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
)

func checksumFor(manifest []byte, assetName string) (string, error) {
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.TrimPrefix(fields[1], "*") != assetName {
			continue
		}
		checksum := strings.ToLower(fields[0])
		decoded, err := hex.DecodeString(checksum)
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("invalid SHA-256 checksum for %s", assetName)
		}
		return checksum, nil
	}
	return "", fmt.Errorf("checksums.txt does not contain %s", assetName)
}

func verifyChecksum(content []byte, expected string) error {
	actual := sha256.Sum256(content)
	if hex.EncodeToString(actual[:]) != strings.ToLower(expected) {
		return fmt.Errorf("SHA-256 checksum verification failed")
	}
	return nil
}

func extractBinary(archive []byte) ([]byte, error) {
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer compressed.Close()

	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if path.Clean(header.Name) != "rakazo-manager" {
			continue
		}
		if !header.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("release archive entry rakazo-manager is not a regular file")
		}
		if header.Size <= 0 || header.Size > maxBinaryFile {
			return nil, fmt.Errorf("release binary has invalid size %d", header.Size)
		}
		binary, err := io.ReadAll(io.LimitReader(reader, maxBinaryFile+1))
		if err != nil {
			return nil, fmt.Errorf("read release binary: %w", err)
		}
		if int64(len(binary)) != header.Size || int64(len(binary)) > maxBinaryFile {
			return nil, fmt.Errorf("release binary is truncated or exceeds the size limit")
		}
		return binary, nil
	}
	return nil, fmt.Errorf("release archive does not contain rakazo-manager")
}
