package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nicolaeser/RakazoManager/internal/manager"
)

func TestJSONWritersProduceDeterministicValidEnvelopes(t *testing.T) {
	status := manager.Status{
		Root:                  "/srv/rakazo/quoted-\"root\"",
		Name:                  "primary",
		Image:                 "registry.example/rakazo@sha256:running",
		TrackedImage:          "registry.example/rakazo:latest",
		WebURL:                "http://127.0.0.1:9119/?value=<safe>",
		WebPort:               9119,
		APIPort:               8642,
		BindAddress:           "127.0.0.1",
		RebuildComposeOnStart: true,
		Data:                  "/srv/rakazo/data",
		Workspace:             "/srv/rakazo/workspace",
		Backups:               "/srv/rakazo/backups",
		Containers:            "running\nhealthy\x1b[31m",
		Version:               "1.2.3",
		APIHealthy:            true,
		APIInfo:               "healthy \"and\" ready",
	}
	report := manager.DoctorReport{Checks: []manager.DoctorCheck{
		{Level: manager.CheckPass, Name: "Storage", Detail: "ready"},
		{Level: manager.CheckWarn, Name: "Path\nlayout", Detail: "contains \\ and \"quotes\""},
	}}
	build := BuildInfo{Version: "v1.2.3", Commit: "abc123", Date: "2026-08-11T12:00:00Z"}

	tests := []struct {
		name    string
		command string
		write   func(*bytes.Buffer) error
	}{
		{name: "status", command: "status", write: func(output *bytes.Buffer) error { return writeStatusJSON(output, status) }},
		{name: "doctor", command: "doctor", write: func(output *bytes.Buffer) error { return writeDoctorJSON(output, report) }},
		{name: "backups", command: "backups", write: func(output *bytes.Buffer) error {
			return writeBackupsJSON(output, []string{"z-last.zip", "a-first.zip"})
		}},
		{name: "version", command: "version", write: func(output *bytes.Buffer) error { return writeVersionJSON(output, build) }},
		{name: "self update check", command: "self-update-check", write: func(output *bytes.Buffer) error {
			return writeSelfUpdateCheckJSON(output, "v1.2.3", "v1.3.0", true)
		}},
		{name: "image update check", command: "image-update-check", write: func(output *bytes.Buffer) error {
			return writeImageUpdateCheckJSON(output, manager.ImageUpdateCheck{
				TrackedImage:     "registry.example/rakazo:latest",
				PinnedImage:      "registry.example/rakazo@sha256:pinned",
				EffectiveImage:   "registry.example/rakazo@sha256:pinned",
				CurrentReference: "registry.example/rakazo@sha256:running",
				CurrentDigest:    "sha256:running",
				CurrentSource:    manager.ImageDigestSourceContainer,
				RemoteDigest:     "sha256:remote",
				UpdateAvailable:  true,
			})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var first bytes.Buffer
			if err := test.write(&first); err != nil {
				t.Fatalf("first write: %v", err)
			}
			var second bytes.Buffer
			if err := test.write(&second); err != nil {
				t.Fatalf("second write: %v", err)
			}
			if first.String() != second.String() {
				t.Fatalf("output is not deterministic:\nfirst:  %q\nsecond: %q", first.String(), second.String())
			}
			if !json.Valid(first.Bytes()) {
				t.Fatalf("invalid JSON: %q", first.String())
			}
			if bytes.Contains(first.Bytes(), []byte("\x1b")) {
				t.Fatalf("raw ANSI escape found in JSON: %q", first.String())
			}
			if !bytes.HasSuffix(first.Bytes(), []byte("\n")) {
				t.Fatalf("JSON output must end with one newline: %q", first.String())
			}

			var envelope struct {
				SchemaVersion int             `json:"schema_version"`
				Command       string          `json:"command"`
				Data          json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(first.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope.SchemaVersion != jsonOutputSchemaVersion {
				t.Fatalf("schema_version = %d, want %d", envelope.SchemaVersion, jsonOutputSchemaVersion)
			}
			if envelope.Command != test.command {
				t.Fatalf("command = %q, want %q", envelope.Command, test.command)
			}
			if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) {
				t.Fatal("data must be present")
			}
		})
	}
}

func TestStatusJSONEscapesControlCharactersWithoutChangingData(t *testing.T) {
	status := manager.Status{
		Root:       "line-one\nline-two\t\"quoted\"\\slash",
		Containers: "red\x1b[31mcontainer\x1b[0m",
		APIInfo:    "<healthy>&ready",
	}
	var output bytes.Buffer
	if err := writeStatusJSON(&output, status); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), status.Root) {
		t.Fatalf("unescaped control characters appeared in JSON: %q", output.String())
	}
	if bytes.Contains(output.Bytes(), []byte("\x1b")) {
		t.Fatalf("raw ANSI escape appeared in JSON: %q", output.String())
	}

	var envelope struct {
		Data statusJSON `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Root != status.Root {
		t.Fatalf("decoded root = %q, want %q", envelope.Data.Root, status.Root)
	}
	if envelope.Data.Containers != status.Containers {
		t.Fatalf("decoded containers = %q, want %q", envelope.Data.Containers, status.Containers)
	}
	if envelope.Data.DashboardHealth.Detail != status.APIInfo {
		t.Fatalf("decoded health detail = %q, want %q", envelope.Data.DashboardHealth.Detail, status.APIInfo)
	}
}

func TestBackupsJSONSortsACopyAndUsesAnEmptyArray(t *testing.T) {
	files := []string{"z.zip", "a.zip"}
	var output bytes.Buffer
	if err := writeBackupsJSON(&output, files); err != nil {
		t.Fatal(err)
	}
	if files[0] != "z.zip" || files[1] != "a.zip" {
		t.Fatalf("input was mutated: %#v", files)
	}
	want := "{\"schema_version\":1,\"command\":\"backups\",\"data\":{\"count\":2,\"files\":[\"a.zip\",\"z.zip\"]}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}

	output.Reset()
	if err := writeBackupsJSON(&output, nil); err != nil {
		t.Fatal(err)
	}
	want = "{\"schema_version\":1,\"command\":\"backups\",\"data\":{\"count\":0,\"files\":[]}}\n"
	if output.String() != want {
		t.Fatalf("empty output = %q, want %q", output.String(), want)
	}
}

func TestVersionJSONStableShape(t *testing.T) {
	var output bytes.Buffer
	if err := writeVersionJSON(&output, BuildInfo{
		Version: "v1.0.0",
		Commit:  "abc123",
		Date:    "2026-08-11T12:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	want := "{\"schema_version\":1,\"command\":\"version\",\"data\":{\"version\":\"v1.0.0\",\"commit\":\"abc123\",\"build_date\":\"2026-08-11T12:00:00Z\"}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

func TestImageUpdateCheckJSONStableShape(t *testing.T) {
	var output bytes.Buffer
	err := writeImageUpdateCheckJSON(&output, manager.ImageUpdateCheck{
		TrackedImage:     "registry.example/rakazo:latest",
		PinnedImage:      "registry.example/rakazo@sha256:pinned",
		EffectiveImage:   "registry.example/rakazo@sha256:pinned",
		CurrentReference: "registry.example/rakazo@sha256:current",
		CurrentDigest:    "sha256:current",
		CurrentSource:    manager.ImageDigestSourceConfiguredImage,
		RemoteDigest:     "sha256:remote",
		UpdateAvailable:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"schema_version\":1,\"command\":\"image-update-check\",\"data\":{\"tracked_image\":\"registry.example/rakazo:latest\",\"pinned_image\":\"registry.example/rakazo@sha256:pinned\",\"effective_image\":\"registry.example/rakazo@sha256:pinned\",\"current_reference\":\"registry.example/rakazo@sha256:current\",\"current_digest\":\"sha256:current\",\"current_source\":\"configured-image\",\"remote_digest\":\"sha256:remote\",\"update_available\":true}}\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if strings.Contains(output.String(), "running_") {
		t.Fatalf("legacy running-image field leaked into output: %q", output.String())
	}
}

func TestJSONEncodingFailureDoesNotWritePartialOutput(t *testing.T) {
	var output bytes.Buffer
	err := writeJSONEnvelope(&output, "invalid", struct {
		Unsupported func() `json:"unsupported"`
	}{Unsupported: func() {}})
	if err == nil {
		t.Fatal("expected encoding error")
	}
	if output.Len() != 0 {
		t.Fatalf("encoding failure wrote partial output: %q", output.String())
	}
}
