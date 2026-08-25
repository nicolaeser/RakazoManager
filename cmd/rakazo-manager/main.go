package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nicolaeser/RakazoManager/internal/app"
)

var (
	version = "v1.0.1"
	commit  = "none"
	date    = "unknown"
)

func main() {
	application := app.New(app.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
	if err := application.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
