package selfupdate

import (
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/nicolaeser/RakazoManager/internal/command"
)

const (
	DefaultRepository = "nicolaeser/RakazoManager"
	defaultAPIBaseURL = "https://api.github.com"

	maxReleaseResponse = 2 << 20
	maxChecksumFile    = 1 << 20
	maxArchiveFile     = 128 << 20
	maxBinaryFile      = 128 << 20
)

type Plan struct {
	CurrentVersion string
	LatestVersion  string
	AssetName      string
	AssetURL       string
	ChecksumsURL   string
	TargetPath     string
	Available      bool
}

type Updater struct {
	Repository string
	APIBaseURL string
	Client     *http.Client
	GOOS       string
	GOARCH     string
	Executable func() (string, error)
	Runner     command.Runner
	In         io.Reader
	Out        io.Writer
	Err        io.Writer
}

func New(runner command.Runner, in io.Reader, out, errOut io.Writer) *Updater {
	repository := strings.TrimSpace(os.Getenv("RAKAZO_MANAGER_REPOSITORY"))
	if repository == "" {
		repository = DefaultRepository
	}
	return &Updater{
		Repository: repository,
		APIBaseURL: defaultAPIBaseURL,
		Client: &http.Client{
			Timeout: 2 * time.Minute,
		},
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Executable: os.Executable,
		Runner:     runner,
		In:         in,
		Out:        out,
		Err:        errOut,
	}
}
