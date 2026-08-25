package manager

import (
	"archive/zip"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveFixtureEntry struct {
	name string
	data string
	mode fs.FileMode
}

func writeArchiveTestEntries(t *testing.T, entries []archiveFixtureEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "archive.zip")
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for _, entry := range entries {
		if err := addBytesToZip(writer, entry.name, []byte(entry.data), entry.mode); err != nil {
			_ = output.Close()
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func writeArchiveTestZIP(t *testing.T, entries map[string]string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "archive.zip")
	output, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(output)
	for name, content := range entries {
		if err := addBytesToZip(writer, name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestValidateZIPEnforcesHeaderAndArchiveLimits(t *testing.T) {
	tests := []struct {
		name    string
		entries map[string]string
		limits  zipLimits
		want    string
	}{
		{
			name:    "member header",
			entries: map[string]string{"large": "12345"},
			limits:  zipLimits{archiveBytes: 1 << 20, memberBytes: 4, totalBytes: 100, members: 10},
			want:    "member limit",
		},
		{
			name:    "total headers",
			entries: map[string]string{"one": "123", "two": "456"},
			limits:  zipLimits{archiveBytes: 1 << 20, memberBytes: 100, totalBytes: 5, members: 10},
			want:    "total uncompressed limit",
		},
		{
			name:    "member count",
			entries: map[string]string{"one": "1", "two": "2", "three": "3"},
			limits:  zipLimits{archiveBytes: 1 << 20, memberBytes: 100, totalBytes: 100, members: 2},
			want:    "limit is 2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archivePath := writeArchiveTestZIP(t, test.entries)
			_, err := inspectZIPWithLimits(archivePath, test.limits, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}

	archivePath := writeArchiveTestZIP(t, map[string]string{"entry": "content"})
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	limits := zipLimits{archiveBytes: info.Size() - 1, memberBytes: 100, totalBytes: 100, members: 10}
	if _, err := inspectZIPWithLimits(archivePath, limits, nil); err == nil || !strings.Contains(err.Error(), "compressed size limit") {
		t.Fatalf("expected compressed archive limit, got %v", err)
	}
}

func TestValidateZIPRejectsSymlinkArchive(t *testing.T) {
	archivePath := writeArchiveTestZIP(t, map[string]string{"entry": "content"})
	symlinkPath := filepath.Join(t.TempDir(), "archive-link.zip")
	if err := os.Symlink(archivePath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if err := ValidateZIP(symlinkPath); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	if _, err := os.Stat(archivePath); err != nil {
		t.Fatalf("source archive unexpectedly changed: %v", err)
	}
}

func TestValidateZIPRejectsUnsafeTypesSymlinksAndMemberGraphs(t *testing.T) {
	tests := []struct {
		name    string
		entries []archiveFixtureEntry
		want    string
	}{
		{
			name: "semantic duplicate",
			entries: []archiveFixtureEntry{
				{"node", "content", 0o600},
				{"node/", "", fs.ModeDir | 0o700},
			},
			want: "duplicate member",
		},
		{
			name: "regular ancestor",
			entries: []archiveFixtureEntry{
				{"node", "content", 0o600},
				{"node/child", "nested", 0o600},
			},
			want: "non-directory ancestor",
		},
		{
			name: "escaping symlink",
			entries: []archiveFixtureEntry{
				{"link", "../outside", fs.ModeSymlink | 0o777},
			},
			want: "escapes the archive root",
		},
		{
			name: "terminal control in member name",
			entries: []archiveFixtureEntry{
				{"unsafe\x1b[2J", "content", 0o600},
			},
			want: "control characters",
		},
		{
			name: "invalid utf8 in member name",
			entries: []archiveFixtureEntry{
				{string([]byte{'b', 'a', 'd', 0xff}), "content", 0o600},
			},
			want: "invalid UTF-8",
		},
		{
			name: "terminal control in symlink target",
			entries: []archiveFixtureEntry{
				{"link", "target\x1b[2J", fs.ModeSymlink | 0o777},
			},
			want: "unsafe target",
		},
		{
			name: "special file",
			entries: []archiveFixtureEntry{
				{"pipe", "", fs.ModeNamedPipe | 0o600},
			},
			want: "unsupported special file type",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archivePath := writeArchiveTestEntries(t, test.entries)
			if err := ValidateZIP(archivePath); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q rejection, got %v", test.want, err)
			}
		})
	}

	valid := writeArchiveTestEntries(t, []archiveFixtureEntry{
		{"target", "content", 0o600},
		{"link", "target", fs.ModeSymlink | 0o777},
	})
	if err := ValidateZIP(valid); err != nil {
		t.Fatalf("safe relative symlink was rejected: %v", err)
	}
}

func TestStageRestoreArchiveValidatesBeforeCopyAndRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	backups := filepath.Join(root, "backups")
	if err := os.Mkdir(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(root, "external")
	if err := os.Mkdir(external, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(external, "invalid.zip")
	if err := os.WriteFile(invalid, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageRestoreArchive(invalid, backups); err == nil {
		t.Fatal("expected invalid external archive rejection")
	}
	if _, err := os.Lstat(filepath.Join(backups, "invalid.zip")); !os.IsNotExist(err) {
		t.Fatalf("invalid archive was copied before validation: %v", err)
	}

	valid := writeArchiveTestZIP(t, map[string]string{"backup.json": "valid"})
	symlink := filepath.Join(external, "linked.zip")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageRestoreArchive(symlink, backups); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected restore symlink rejection, got %v", err)
	}

	validExternal := filepath.Join(external, "valid.zip")
	content, err := os.ReadFile(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(validExternal, content, 0o600); err != nil {
		t.Fatal(err)
	}
	staged, name, err := stageRestoreArchive(validExternal, backups)
	if err != nil {
		t.Fatal(err)
	}
	if name != "valid.zip" || staged != filepath.Join(backups, name) {
		t.Fatalf("unexpected staged restore: path=%s name=%s", staged, name)
	}
	if err := ValidateZIP(staged); err != nil {
		t.Fatalf("staged valid archive failed validation: %v", err)
	}

	unsafeName := filepath.Join(external, "unsafe\x1b[2J.zip")
	if err := os.WriteFile(unsafeName, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageRestoreArchive(unsafeName, backups); err == nil || !strings.Contains(err.Error(), "unsafe backup filename") {
		t.Fatalf("expected terminal-control backup filename rejection, got %v", err)
	}
}

func TestRestoreRejectsInvalidArchiveBeforeRuntimeMutation(t *testing.T) {
	runner := &adminTestRunner{running: false, exists: true}
	manager, _ := newInstalledAdminManager(t, runner, freeAdminPort(t), freeAdminPort(t))
	invalid := filepath.Join(t.TempDir(), "invalid.zip")
	if err := os.WriteFile(invalid, []byte("not a ZIP"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(context.Background(), invalid); err == nil {
		t.Fatal("expected invalid restore rejection")
	}
	if len(runner.requests) != 0 {
		t.Fatalf("invalid restore changed or inspected Docker runtime: %#v", runner.requests)
	}
	if _, err := os.Lstat(filepath.Join(manager.Paths.Backups, filepath.Base(invalid))); !os.IsNotExist(err) {
		t.Fatalf("invalid restore was staged: %v", err)
	}
}
