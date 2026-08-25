package stack

import (
	"fmt"
	"path/filepath"
)

type Paths struct {
	Root          string
	Compose       string
	Manager       string
	Config        string
	Secrets       string
	State         string
	OperationsLog string
	Lock          string
	Schedule      string
	ScheduleLog   string
	Data          string
	Postgres      string
	App           string
	Workspace     string
	Backups       string
}

func NewPaths(root string) (Paths, error) {
	if root == "" {
		return Paths{}, fmt.Errorf("instance folder cannot be empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve instance folder: %w", err)
	}
	absolute = filepath.Clean(absolute)
	manager := filepath.Join(absolute, ".manager")
	data := filepath.Join(absolute, "data")
	return Paths{
		Root:          absolute,
		Compose:       filepath.Join(absolute, "docker-compose.yml"),
		Manager:       manager,
		Config:        filepath.Join(manager, "instance.json"),
		Secrets:       filepath.Join(manager, "secrets.env"),
		State:         filepath.Join(manager, "state.json"),
		OperationsLog: filepath.Join(manager, "operations.log"),
		Lock:          filepath.Join(manager, "operation.lock"),
		Schedule:      filepath.Join(manager, "schedule.json"),
		ScheduleLog:   filepath.Join(manager, "schedule.log"),
		Data:          data,
		Postgres:      filepath.Join(absolute, "pg"),
		App:           data,
		Workspace:     filepath.Join(absolute, "workspace"),
		Backups:       filepath.Join(absolute, "backups"),
	}, nil
}

func (p Paths) LegacyCompose() string {
	return filepath.Join(p.Root, "compose.yaml")
}

func (p Paths) ComposeOverride() string {
	return filepath.Join(p.Root, "docker-compose.override.yml")
}

func (p Paths) GetData() string      { return p.Data }
func (p Paths) GetWorkspace() string { return p.Workspace }
func (p Paths) GetBackups() string   { return p.Backups }
