package app

import (
	"context"
	"flag"
	"fmt"

	"github.com/nicolaeser/RakazoManager/internal/ui"
)

func (app *App) versionCommand(args []string) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("usage: rakazo-manager version [--json]")
	}
	if *jsonOutput {
		return writeVersionJSON(app.Out, app.Build)
	}
	fmt.Fprintf(app.Out, "Rakazo Manager %s\ncommit: %s\nbuilt: %s\n", app.Build.Version, app.Build.Commit, app.Build.Date)
	return nil
}

func (app *App) updateCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	if len(args) > 0 && args[0] == "check" {
		return app.imageUpdateCheckCommand(ctx, terminal, args[1:])
	}
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	dryRun := flags.Bool("dry-run", false, "print the update plan and planned Compose file without changing anything")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager update [--dry-run] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	if *dryRun {
		preview, previewErr := rt.manager.PreviewUpdate(ctx)
		if previewErr != nil {
			return previewErr
		}
		terminal.Section("Update dry run — no changes made")
		terminal.KeyValue("Folder", preview.Root)
		terminal.KeyValue("Tracked image", preview.TrackedImage)
		terminal.KeyValue("Effective image", preview.EffectiveImage)
		terminal.KeyValue("Container running", preview.ContainerRunning)
		if preview.RunningImage != "" {
			terminal.KeyValue("Running image", preview.RunningImage)
		}
		for _, action := range preview.Actions {
			terminal.Info("Would %s", action)
		}
		terminal.Section("Planned managed Compose state")
		fmt.Fprint(app.Out, preview.Compose)
		return nil
	}
	terminal.Info("Update recreates the container image only; host data/ is bind-mounted and kept.")
	confirmed, err := terminal.Confirm("Create a backup, pull the newest image, and update Rakazo?", true)
	if err != nil {
		return err
	}
	if !confirmed {
		return fmt.Errorf("update cancelled")
	}
	return rt.withSuccess("Update completed and verified; host data was preserved", rt.manager.Update(ctx))
}

func (app *App) imageUpdateCheckCommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("update check", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager update check [--json] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	check, err := rt.manager.CheckImageUpdate(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeImageUpdateCheckJSON(app.Out, check)
	}
	terminal.Section("Rakazo image update check")
	terminal.KeyValue("Tracked image", check.TrackedImage)
	if check.PinnedImage != "" {
		terminal.KeyValue("Rollback pin", check.PinnedImage)
	}
	terminal.KeyValue("Current source", check.CurrentSource)
	terminal.KeyValue("Current image", check.CurrentReference)
	terminal.KeyValue("Current digest", check.CurrentDigest)
	terminal.KeyValue("Remote digest", check.RemoteDigest)
	if check.UpdateAvailable {
		terminal.Warn("A newer or different manifest is available for the tracked image.")
		return nil
	}
	terminal.Success("The current image manifest matches the registry")
	return nil
}

func (app *App) statusCLICommand(ctx context.Context, terminal *ui.UI, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(app.Err)
	jsonOutput := flags.Bool("json", false, "write stable machine-readable JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	root, err := oneFolder(flags.Args())
	if err != nil {
		return fmt.Errorf("usage: rakazo-manager status [--json] [FOLDER]")
	}
	rt, err := app.runtime(root, terminal)
	if err != nil {
		return err
	}
	if !*jsonOutput {
		return rt.statusCommand(ctx)
	}
	status, err := rt.manager.Status(ctx)
	if err != nil {
		return err
	}
	return writeStatusJSON(app.Out, status)
}
