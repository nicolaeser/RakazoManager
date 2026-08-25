package app

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/nicolaeser/RakazoManager/internal/manager"
)

const jsonOutputSchemaVersion = 1

type jsonEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	Command       string `json:"command"`
	Data          any    `json:"data"`
}

type statusJSON struct {
	Root                  string              `json:"root"`
	Name                  string              `json:"name"`
	Image                 string              `json:"image"`
	TrackedImage          string              `json:"tracked_image"`
	DashboardURL          string              `json:"web_url"`
	WebPort               int                 `json:"web_port"`
	APIPort               int                 `json:"api_port"`
	BindAddress           string              `json:"bind_address"`
	RebuildComposeOnStart bool                `json:"rebuild_compose_on_start"`
	Data                  string              `json:"data_path"`
	Workspace             string              `json:"workspace_path"`
	Backups               string              `json:"backups_path"`
	Containers            string              `json:"containers"`
	Version               string              `json:"revision"`
	DashboardHealth       dashboardHealthJSON `json:"api_health"`
}

type dashboardHealthJSON struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type doctorJSON struct {
	Healthy bool              `json:"healthy"`
	Checks  []doctorCheckJSON `json:"checks"`
}

type doctorCheckJSON struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type backupsJSON struct {
	Count int      `json:"count"`
	Files []string `json:"files"`
}

type versionJSON struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

type selfUpdateCheckJSON struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
}

type imageUpdateCheckJSON struct {
	TrackedImage     string `json:"tracked_image"`
	PinnedImage      string `json:"pinned_image"`
	EffectiveImage   string `json:"effective_image"`
	CurrentReference string `json:"current_reference"`
	CurrentDigest    string `json:"current_digest"`
	CurrentSource    string `json:"current_source"`
	RemoteDigest     string `json:"remote_digest"`
	UpdateAvailable  bool   `json:"update_available"`
}

func writeStatusJSON(output io.Writer, status manager.Status) error {
	data := statusJSON{
		Root:                  status.Root,
		Name:                  status.Name,
		Image:                 status.Image,
		TrackedImage:          status.TrackedImage,
		DashboardURL:          status.WebURL,
		WebPort:               status.WebPort,
		APIPort:               status.APIPort,
		BindAddress:           status.BindAddress,
		RebuildComposeOnStart: status.RebuildComposeOnStart,
		Data:                  status.Data,
		Workspace:             status.Workspace,
		Backups:               status.Backups,
		Containers:            status.Containers,
		Version:               status.Version,
		DashboardHealth: dashboardHealthJSON{
			OK:     status.APIHealthy,
			Detail: status.APIInfo,
		},
	}
	return writeJSONEnvelope(output, "status", data)
}

func writeDoctorJSON(output io.Writer, report manager.DoctorReport) error {
	checks := make([]doctorCheckJSON, len(report.Checks))
	for index, check := range report.Checks {
		checks[index] = doctorCheckJSON{
			Level:  string(check.Level),
			Name:   check.Name,
			Detail: check.Detail,
		}
	}
	return writeJSONEnvelope(output, "doctor", doctorJSON{
		Healthy: report.Healthy(),
		Checks:  checks,
	})
}

func writeBackupsJSON(output io.Writer, files []string) error {
	ordered := append([]string(nil), files...)
	sort.Strings(ordered)
	if ordered == nil {
		ordered = []string{}
	}
	return writeJSONEnvelope(output, "backups", backupsJSON{
		Count: len(ordered),
		Files: ordered,
	})
}

func writeVersionJSON(output io.Writer, build BuildInfo) error {
	return writeJSONEnvelope(output, "version", versionJSON{
		Version:   build.Version,
		Commit:    build.Commit,
		BuildDate: build.Date,
	})
}

func writeSelfUpdateCheckJSON(
	output io.Writer,
	currentVersion string,
	latestVersion string,
	updateAvailable bool,
) error {
	return writeJSONEnvelope(output, "self-update-check", selfUpdateCheckJSON{
		CurrentVersion:  currentVersion,
		LatestVersion:   latestVersion,
		UpdateAvailable: updateAvailable,
	})
}

func writeImageUpdateCheckJSON(output io.Writer, check manager.ImageUpdateCheck) error {
	return writeJSONEnvelope(output, "image-update-check", imageUpdateCheckJSON{
		TrackedImage:     check.TrackedImage,
		PinnedImage:      check.PinnedImage,
		EffectiveImage:   check.EffectiveImage,
		CurrentReference: check.CurrentReference,
		CurrentDigest:    check.CurrentDigest,
		CurrentSource:    check.CurrentSource,
		RemoteDigest:     check.RemoteDigest,
		UpdateAvailable:  check.UpdateAvailable,
	})
}

func writeJSONEnvelope(output io.Writer, command string, data any) error {
	if output == nil {
		return fmt.Errorf("write %s JSON: output is not configured", command)
	}
	encoded, err := json.Marshal(jsonEnvelope{
		SchemaVersion: jsonOutputSchemaVersion,
		Command:       command,
		Data:          data,
	})
	if err != nil {
		return fmt.Errorf("encode %s JSON: %w", command, err)
	}
	encoded = append(encoded, '\n')
	written, err := output.Write(encoded)
	if err != nil {
		return fmt.Errorf("write %s JSON: %w", command, err)
	}
	if written != len(encoded) {
		return fmt.Errorf("write %s JSON: %w", command, io.ErrShortWrite)
	}
	return nil
}
