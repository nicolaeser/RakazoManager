package app

import (
	"context"
	"flag"
	"fmt"

	"github.com/nicolaeser/RakazoManager/internal/selfupdate"
	"github.com/nicolaeser/RakazoManager/internal/ui"
)

func (app *App) selfUpdateCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("self-update", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	checkOnly := flags.Bool("check", false, "check for a newer release without installing it")
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON (requires --check)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("usage: rakazo-manager self-update [--check] [--json]")
	}
	if *jsonOutput && !*checkOnly {
		return fmt.Errorf("self-update --json requires --check")
	}

	updater := selfupdate.New(app.Runner, app.In, app.Out, app.Err)
	if !*jsonOutput {
		terminal.Info("Checking GitHub for the latest Rakazo Manager release")
	}
	plan, err := updater.Check(ctx, app.Build.Version)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeSelfUpdateCheckJSON(app.Out, plan.CurrentVersion, plan.LatestVersion, plan.Available)
	}

	terminal.Section("Rakazo Manager update")
	terminal.KeyValue("Installed", plan.CurrentVersion)
	terminal.KeyValue("Latest", plan.LatestVersion)
	terminal.KeyValue("Executable", plan.TargetPath)
	if !plan.Available {
		terminal.Success("Rakazo Manager is already up to date")
		return nil
	}
	if *checkOnly {
		terminal.Warn("A newer Rakazo Manager release is available.")
		return nil
	}

	confirmed, err := terminal.Confirm(
		fmt.Sprintf("Download verified release %s and replace the installed Rakazo Manager?", plan.LatestVersion),
		false,
	)
	if err != nil {
		return err
	}
	if !confirmed {
		terminal.Info("Self-update cancelled")
		return nil
	}
	terminal.Info("Downloading %s and verifying its SHA-256 checksum", plan.AssetName)
	if err := updater.Apply(ctx, plan); err != nil {
		return err
	}
	terminal.Success("Rakazo Manager updated to %s", plan.LatestVersion)
	terminal.Info("The new version is used the next time you run rakazo-manager.")
	return nil
}
