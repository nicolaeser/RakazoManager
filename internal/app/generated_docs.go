package app

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

type generatedCommandSpec struct {
	Name        string
	Usage       string
	Summary     string
	Options     []string
	OptionLines []generatedOptionSpec
	Subcommands []generatedSubcommandSpec
}

type generatedSubcommandSpec struct {
	Name        string
	Usage       string
	Summary     string
	Options     []string
	OptionLines []generatedOptionSpec
}

type generatedOptionSpec struct {
	Usage       string
	Description string
}

type generatedCompletionValueKind string

const (
	completionValueString      generatedCompletionValueKind = "string"
	completionValueInteger     generatedCompletionValueKind = "integer"
	completionValueBoolean     generatedCompletionValueKind = "boolean"
	completionValueBindAddress generatedCompletionValueKind = "bind-address"
	completionValueTime        generatedCompletionValueKind = "time"
	completionValueCron        generatedCompletionValueKind = "cron"
	completionValueDirectory   generatedCompletionValueKind = "directory"
	completionValueFile        generatedCompletionValueKind = "file"
	completionValueZIP         generatedCompletionValueKind = "zip"
	completionValueShell       generatedCompletionValueKind = "shell"
)

type generatedCompletionOperand struct {
	Name string
	Kind generatedCompletionValueKind
}

type generatedCompletionSpec struct {
	Operands     []generatedCompletionOperand
	OptionValues map[string]generatedCompletionValueKind
}

var generatedCompletionSpecs = map[string]generatedCompletionSpec{
	"install": {
		Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}},
		OptionValues: map[string]generatedCompletionValueKind{
			"--name": completionValueString, "--image": completionValueString,
			"--web-port": completionValueInteger, "--api-port": completionValueInteger,
			"--postgres-port": completionValueInteger, "--origin": completionValueString,
			"--openrouter-key-file": completionValueFile,
		},
	},
	"instances list": {Operands: []generatedCompletionOperand{{Name: "parent", Kind: completionValueDirectory}}},
	"config show":    {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"config set": {
		Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}},
		OptionValues: map[string]generatedCompletionValueKind{
			"--name": completionValueString, "--image": completionValueString,
			"--web-port": completionValueInteger, "--api-port": completionValueInteger,
			"--postgres-port": completionValueInteger, "--bind-address": completionValueBindAddress,
			"--origin": completionValueString, "--rebuild-on-start": completionValueBoolean,
		},
	},
	"import-instance": {
		Operands: []generatedCompletionOperand{{Name: "export", Kind: completionValueZIP}, {Name: "folder", Kind: completionValueDirectory}},
		OptionValues: map[string]generatedCompletionValueKind{
			"--name": completionValueString, "--image": completionValueString,
			"--web-port": completionValueInteger, "--api-port": completionValueInteger,
			"--postgres-port": completionValueInteger, "--bind-address": completionValueBindAddress,
			"--origin": completionValueString,
		},
	},
	"start":           {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"stop":            {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"restart":         {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"status":          {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"logs":            {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}, OptionValues: map[string]generatedCompletionValueKind{"--tail": completionValueInteger}},
	"shell":           {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"open":            {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"backups":         {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"backups prune":   {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}, OptionValues: map[string]generatedCompletionValueKind{"--keep": completionValueInteger}},
	"backup":          {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}, OptionValues: map[string]generatedCompletionValueKind{"--label": completionValueString}},
	"export-instance": {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"restore":         {Operands: []generatedCompletionOperand{{Name: "backup", Kind: completionValueZIP}, {Name: "folder", Kind: completionValueDirectory}}},
	"update":          {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"update check":    {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"rollback":        {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"doctor":          {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"schedule set": {
		Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}},
		OptionValues: map[string]generatedCompletionValueKind{
			"--daily": completionValueTime, "--cron": completionValueCron, "--keep": completionValueInteger,
		},
	},
	"schedule show":   {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"schedule remove": {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"schedule run":    {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}, OptionValues: map[string]generatedCompletionValueKind{"--keep": completionValueInteger}},
	"decommission": {
		Operands:     []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}},
		OptionValues: map[string]generatedCompletionValueKind{"--confirm-delete-data": completionValueString},
	},
	"menu":       {Operands: []generatedCompletionOperand{{Name: "folder", Kind: completionValueDirectory}}},
	"completion": {Operands: []generatedCompletionOperand{{Name: "shell", Kind: completionValueShell}}},
}

var generatedCommandSpecs = []generatedCommandSpec{
	{Name: "install", Usage: "[options] [folder]", Summary: "Create or repair an instance", Options: []string{"--name", "--image", "--web-port", "--api-port", "--postgres-port", "--bind-all", "--origin", "--no-pull", "--no-start", "--rebuild-on-start", "--openrouter-key-file", "--dry-run"}, OptionLines: []generatedOptionSpec{
		{Usage: "--name NAME", Description: "Override the derived container and project name."},
		{Usage: "--image IMAGE", Description: "Override the tracked Rakazo image."},
		{Usage: "--web-port PORT", Description: "Select the web host port; zero selects automatically."},
		{Usage: "--api-port PORT", Description: "Select the API host port; zero selects automatically."},
		{Usage: "--postgres-port PORT", Description: "Select the Postgres host port; zero selects automatically."},
		{Usage: "--bind-all", Description: "Publish the web UI on every host interface; API and Postgres stay on 127.0.0.1."},
		{Usage: "--origin URL", Description: "Public origin for Caddy and auth. An IP becomes http://IP:<web-port>; a domain becomes https://DOMAIN. Blank keeps http://127.0.0.1:<web-port>."},
		{Usage: "--no-pull", Description: "Do not pull the configured image."},
		{Usage: "--no-start", Description: "Prepare the instance without starting it."},
		{Usage: "--rebuild-on-start", Description: "Regenerate the base Compose file on every start."},
		{Usage: "--openrouter-key-file FILE", Description: "Optional OpenRouter API key file; '-' reads standard input."},
		{Usage: "--dry-run", Description: "Print the planned configuration, Compose file, and actions without changing anything."},
	}},
	{Name: "instances", Summary: "Discover managed instances", Subcommands: []generatedSubcommandSpec{
		{Name: "list", Usage: "list [--json] [parent]", Summary: "List managed instances under a parent folder", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
			{Usage: "--json", Description: "Write the stable machine-readable result."},
		}},
	}},
	{Name: "config", Summary: "Inspect or change managed instance configuration", Subcommands: []generatedSubcommandSpec{
		{Name: "show", Usage: "show [--json] [folder]", Summary: "Show the effective managed configuration", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
			{Usage: "--json", Description: "Write the stable machine-readable result."},
		}},
		{Name: "set", Usage: "set [options] [folder]", Summary: "Change managed configuration", Options: []string{"--name", "--image", "--web-port", "--api-port", "--postgres-port", "--bind-address", "--origin", "--rebuild-on-start", "--dry-run"}, OptionLines: []generatedOptionSpec{
			{Usage: "--name NAME", Description: "Set the managed instance name."},
			{Usage: "--image IMAGE", Description: "Set the tracked Rakazo image."},
			{Usage: "--web-port PORT", Description: "Set the web host port."},
			{Usage: "--api-port PORT", Description: "Set the API host port."},
			{Usage: "--postgres-port PORT", Description: "Set the Postgres host port."},
			{Usage: "--bind-address ADDRESS", Description: "Set the host bind address."},
			{Usage: "--origin URL", Description: "Set the public origin. An IP becomes http://IP:<web-port>; empty restores the localhost origin."},
			{Usage: "--rebuild-on-start=BOOL", Description: "Enable or disable base Compose regeneration on start."},
			{Usage: "--dry-run", Description: "Print the planned configuration and actions without changing anything."},
		}},
	}},
	{Name: "import-instance", Usage: "[options] EXPORT.zip [folder]", Summary: "Create an instance from a disaster-recovery export", Options: []string{"--name", "--image", "--web-port", "--api-port", "--postgres-port", "--bind-address", "--origin", "--no-pull", "--no-start", "--dry-run"}, OptionLines: []generatedOptionSpec{
		{Usage: "--name NAME", Description: "Override the imported instance name."},
		{Usage: "--image IMAGE", Description: "Override the imported tracked image."},
		{Usage: "--web-port PORT", Description: "Override the imported web host port."},
		{Usage: "--api-port PORT", Description: "Override the imported API host port."},
		{Usage: "--postgres-port PORT", Description: "Override the imported Postgres host port."},
		{Usage: "--bind-address ADDRESS", Description: "Override the imported host bind address."},
		{Usage: "--origin URL", Description: "Override the imported public origin."},
		{Usage: "--no-pull", Description: "Do not pull the configured image."},
		{Usage: "--no-start", Description: "Import without starting the instance."},
		{Usage: "--dry-run", Description: "Validate the export and print planned actions without changing anything."},
	}},
	{Name: "start", Usage: "[--rebuild] [folder]", Summary: "Start the instance", Options: []string{"--rebuild"}, OptionLines: []generatedOptionSpec{
		{Usage: "--rebuild", Description: "Force regeneration of the base Compose file for this start."},
	}},
	{Name: "stop", Usage: "[folder]", Summary: "Stop the instance without deleting data"},
	{Name: "restart", Usage: "[folder]", Summary: "Restart the instance"},
	{Name: "status", Usage: "[--json] [folder]", Summary: "Show image, ports, paths, and container state", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
		{Usage: "--json", Description: "Write the stable machine-readable result."},
	}},
	{Name: "logs", Usage: "[--tail N] [folder]", Summary: "Follow container logs", Options: []string{"--tail"}, OptionLines: []generatedOptionSpec{
		{Usage: "--tail N", Description: "Include the newest N existing log lines before following."},
	}},
	{Name: "shell", Usage: "[folder] [-- COMMAND...]", Summary: "Open a shell or run a command in the container"},
	{Name: "open", Usage: "[folder]", Summary: "Show the local web and API health URLs"},
	{Name: "backups", Usage: "[--json] [folder]", Summary: "List or prune backup archives", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
		{Usage: "--json", Description: "Write the stable machine-readable backup list."},
	}, Subcommands: []generatedSubcommandSpec{
		{Name: "prune", Usage: "prune [--keep N] [--dry-run] [folder]", Summary: "Remove old automatic safety backups", Options: []string{"--keep", "--dry-run"}, OptionLines: []generatedOptionSpec{
			{Usage: "--keep N", Description: "Retain the newest N automatic safety backups."},
			{Usage: "--dry-run", Description: "List matching backups without deleting them."},
		}},
	}},
	{Name: "backup", Usage: "[--label NAME] [folder]", Summary: "Create and verify a backup", Options: []string{"--label"}, OptionLines: []generatedOptionSpec{
		{Usage: "--label NAME", Description: "Set the short label embedded in the archive name."},
	}},
	{Name: "export-instance", Usage: "[--without-workspace] [folder]", Summary: "Create a disaster-recovery export", Options: []string{"--without-workspace"}, OptionLines: []generatedOptionSpec{
		{Usage: "--without-workspace", Description: "Exclude project workspace files from the export."},
	}},
	{Name: "restore", Usage: "[--dry-run] BACKUP.zip [folder]", Summary: "Restore an archive after making a safety backup", Options: []string{"--dry-run"}, OptionLines: []generatedOptionSpec{
		{Usage: "--dry-run", Description: "Validate and describe the restore without changing the instance."},
	}},
	{Name: "update", Usage: "[--dry-run] [folder]", Summary: "Back up, pull, recreate, and verify the instance", Options: []string{"--dry-run"}, OptionLines: []generatedOptionSpec{
		{Usage: "--dry-run", Description: "Print the planned update actions without changing the instance."},
	}, Subcommands: []generatedSubcommandSpec{
		{Name: "check", Usage: "check [--json] [folder]", Summary: "Compare the running and remote image digests", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
			{Usage: "--json", Description: "Write the stable machine-readable result."},
		}},
	}},
	{Name: "rollback", Usage: "[folder]", Summary: "Recreate the instance with its previous image"},
	{Name: "doctor", Usage: "[--json] [--clear-stale-lock] [folder]", Summary: "Validate storage, mounts, Docker, and API health", Options: []string{"--json", "--clear-stale-lock"}, OptionLines: []generatedOptionSpec{
		{Usage: "--json", Description: "Write the stable machine-readable report."},
		{Usage: "--clear-stale-lock", Description: "Remove a leftover operation lock only when its process is not alive."},
	}},
	{Name: "schedule", Summary: "Manage scheduled backups", Subcommands: []generatedSubcommandSpec{
		{Name: "set", Usage: "set [--daily HH:MM | --cron EXPR] [--keep N] [folder]", Summary: "Install or replace the backup schedule", Options: []string{"--daily", "--cron", "--keep"}, OptionLines: []generatedOptionSpec{
			{Usage: "--daily HH:MM", Description: "Run the scheduled backup every day at the local time."},
			{Usage: "--cron EXPR", Description: "Use an advanced five-field cron expression."},
			{Usage: "--keep N", Description: "Retain the newest N scheduled backups."},
		}},
		{Name: "show", Usage: "show [--json] [folder]", Summary: "Show the installed backup schedule", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
			{Usage: "--json", Description: "Write the stable machine-readable backup schedule."},
		}},
		{Name: "remove", Usage: "remove [folder]", Summary: "Remove the installed backup schedule"},
		{Name: "run", Usage: "run [--keep N] [folder]", Summary: "Run the scheduled backup procedure now", Options: []string{"--keep"}, OptionLines: []generatedOptionSpec{
			{Usage: "--keep N", Description: "Retain the newest N scheduled backups."},
		}},
	}},
	{Name: "decommission", Usage: "[options] [folder]", Summary: "Remove managed runtime resources with explicit data controls", Options: []string{"--delete-data", "--delete-workspace", "--delete-backups", "--delete-override", "--confirm-delete-data", "--dry-run"}, OptionLines: []generatedOptionSpec{
		{Usage: "--delete-data", Description: "Delete the critical application data directory."},
		{Usage: "--delete-workspace", Description: "Delete the optional project workspace."},
		{Usage: "--delete-backups", Description: "Delete stored backup archives."},
		{Usage: "--delete-override", Description: "Delete the user-owned Compose override."},
		{Usage: "--confirm-delete-data NAME", Description: "Supply the exact instance name required for data deletion."},
		{Usage: "--dry-run", Description: "Print the resources and actions without deleting anything."},
	}},
	{Name: "self-update", Usage: "[--check] [--json]", Summary: "Check for or install a Rakazo Manager release", Options: []string{"--check", "--json"}, OptionLines: []generatedOptionSpec{
		{Usage: "--check", Description: "Check for a newer release without installing it."},
		{Usage: "--json", Description: "Write the stable machine-readable check result; requires --check."},
	}},
	{Name: "menu", Usage: "[folder]", Summary: "Open the interactive menu"},
	{Name: "version", Usage: "[--json]", Summary: "Show build information", Options: []string{"--json"}, OptionLines: []generatedOptionSpec{
		{Usage: "--json", Description: "Write the stable machine-readable build information."},
	}},
	{Name: "completion", Usage: "bash|zsh|fish", Summary: "Generate shell completion for bash, zsh, or fish"},
	{Name: "man", Summary: "Generate this manual in roff format"},
	{Name: "help", Summary: "Show command help"},
}

func (app *App) completionCommand(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: rakazo-manager completion bash|zsh|fish")
	}
	script, err := completionScript(args[0])
	if err != nil {
		return err
	}
	return writeGeneratedOutput(app.Out, "completion", script)
}

func (app *App) manCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: rakazo-manager man")
	}
	return writeGeneratedOutput(app.Out, "manual", manPage(app.Build))
}

func completionScript(shell string) (string, error) {
	switch shell {
	case "bash":
		return bashCompletion(), nil
	case "zsh":
		return zshCompletion(), nil
	case "fish":
		return fishCompletion(), nil
	default:
		return "", fmt.Errorf("unsupported completion shell %q; choose bash, zsh, or fish", shell)
	}
}

func bashCompletion() string {
	var output strings.Builder
	output.WriteString("# bash completion for rakazo-manager\n")
	output.WriteString("_rakazo_manager() {\n")
	output.WriteString("  local current previous command subcommand options word value i command_index start positional skip_next\n")
	output.WriteString("  current=\"${COMP_WORDS[COMP_CWORD]}\"\n")
	output.WriteString("  previous=\"\"\n")
	output.WriteString("  if ((COMP_CWORD > 0)); then previous=\"${COMP_WORDS[COMP_CWORD-1]}\"; fi\n")
	output.WriteString("  command=\"\"\n")
	output.WriteString("  subcommand=\"\"\n")
	output.WriteString("  command_index=0\n")
	output.WriteString("  for ((i = 1; i < COMP_CWORD; i++)); do\n")
	output.WriteString("    word=\"${COMP_WORDS[i]}\"\n")
	output.WriteString("    case \"$word\" in\n")
	output.WriteString("      --yes|--no-color|-h|--help) ;;\n")
	output.WriteString("      *) command=\"$word\"; command_index=$i; break ;;\n")
	output.WriteString("    esac\n")
	output.WriteString("  done\n")
	output.WriteString("  if [[ -z \"$command\" ]]; then\n")
	fmt.Fprintf(&output, "    COMPREPLY=( $(compgen -W %s -- \"$current\") )\n", bashQuote("--yes --no-color -h --help "+generatedCommandNames()))
	output.WriteString("    return\n")
	output.WriteString("  fi\n")
	output.WriteString("  if ((COMP_CWORD == command_index + 1)) && [[ \"$current\" != -* ]]; then\n")
	output.WriteString("    case \"$command\" in\n")
	for _, command := range generatedCommandSpecs {
		if len(command.Subcommands) == 0 {
			continue
		}
		fmt.Fprintf(&output, "      %s)\n", command.Name)
		fmt.Fprintf(&output, "        COMPREPLY=( $(compgen -W %s -- \"$current\") )\n", bashQuote(generatedSubcommandNames(command)))
		if firstOperandKind(completionSpecFor(command.Name, "")) == completionValueDirectory {
			output.WriteString("        while IFS= read -r word; do COMPREPLY+=(\"$word\"); done < <(compgen -d -- \"$current\")\n")
			output.WriteString("        compopt +o default +o bashdefault 2>/dev/null\n")
			output.WriteString("        compopt -o filenames 2>/dev/null\n")
		}
		output.WriteString("        return ;;\n")
	}
	output.WriteString("    esac\n")
	output.WriteString("  fi\n")
	output.WriteString("  if ((COMP_CWORD > command_index + 1)); then\n")
	output.WriteString("    word=\"${COMP_WORDS[command_index + 1]}\"\n")
	output.WriteString("    case \"$command:$word\" in\n")
	for _, command := range generatedCommandSpecs {
		for _, subcommand := range command.Subcommands {
			fmt.Fprintf(&output, "      %s:%s) subcommand=\"%s\" ;;\n", command.Name, subcommand.Name, subcommand.Name)
		}
	}
	output.WriteString("    esac\n")
	output.WriteString("  fi\n")
	output.WriteString("  options=''\n")
	output.WriteString("  case \"$command:$subcommand\" in\n")
	for _, command := range generatedCommandSpecs {
		if len(command.Options) > 0 {
			fmt.Fprintf(&output, "    %s:) options=\"$options %s\" ;;\n", command.Name, strings.Join(command.Options, " "))
		}
		for _, subcommand := range command.Subcommands {
			if len(subcommand.Options) == 0 {
				continue
			}
			fmt.Fprintf(&output, "    %s:%s) options=\"$options %s\" ;;\n", command.Name, subcommand.Name, strings.Join(subcommand.Options, " "))
		}
	}
	output.WriteString("  esac\n")
	output.WriteString("  case \"$command:$subcommand:$current\" in\n")
	for _, path := range generatedCommandPaths() {
		for _, completionOption := range completionOptions(path) {
			option := completionOption.Name
			kind := completionOption.Kind
			choices := completionChoices(kind)
			if len(choices) == 0 {
				continue
			}
			fmt.Fprintf(&output, "    %s:%s:%s=*)\n", path.Command, path.Subcommand, option)
			output.WriteString("      value=\"${current#*=}\"\n")
			fmt.Fprintf(&output, "      COMPREPLY=( $(compgen -W %s -- \"$value\") )\n", bashQuote(strings.Join(choices, " ")))
			fmt.Fprintf(&output, "      for i in \"${!COMPREPLY[@]}\"; do COMPREPLY[$i]=\"%s${COMPREPLY[$i]}\"; done\n", option+"=")
			output.WriteString("      compopt +o default +o bashdefault 2>/dev/null\n")
			output.WriteString("      return ;;\n")
		}
	}
	output.WriteString("  esac\n")
	output.WriteString("  case \"$command:$subcommand:$previous\" in\n")
	for _, path := range generatedCommandPaths() {
		for _, completionOption := range completionOptions(path) {
			option := completionOption.Name
			kind := completionOption.Kind
			if kind == completionValueBoolean {
				continue
			}
			fmt.Fprintf(&output, "    %s:%s:%s)\n", path.Command, path.Subcommand, option)
			if choices := completionChoices(kind); len(choices) > 0 {
				fmt.Fprintf(&output, "      COMPREPLY=( $(compgen -W %s -- \"$current\") )\n", bashQuote(strings.Join(choices, " ")))
			}
			output.WriteString("      compopt +o default +o bashdefault 2>/dev/null\n")
			output.WriteString("      return ;;\n")
		}
	}
	output.WriteString("  esac\n")
	output.WriteString("  start=$((command_index + 1))\n")
	output.WriteString("  if [[ -n \"$subcommand\" ]]; then start=$((start + 1)); fi\n")
	output.WriteString("  positional=0\n")
	output.WriteString("  skip_next=0\n")
	output.WriteString("  for ((i = start; i < COMP_CWORD; i++)); do\n")
	output.WriteString("    word=\"${COMP_WORDS[i]}\"\n")
	output.WriteString("    if ((skip_next)); then skip_next=0; continue; fi\n")
	output.WriteString("    if [[ \"$word\" == --*=* ]]; then continue; fi\n")
	output.WriteString("    if [[ \"$word\" == -* ]]; then\n")
	output.WriteString("      case \"$command:$subcommand:$word\" in\n")
	for _, path := range generatedCommandPaths() {
		for _, completionOption := range completionOptions(path) {
			option := completionOption.Name
			kind := completionOption.Kind
			if kind == completionValueBoolean {
				continue
			}
			fmt.Fprintf(&output, "        %s:%s:%s) skip_next=1 ;;\n", path.Command, path.Subcommand, option)
		}
	}
	output.WriteString("      esac\n")
	output.WriteString("      continue\n")
	output.WriteString("    fi\n")
	output.WriteString("    positional=$((positional + 1))\n")
	output.WriteString("  done\n")
	output.WriteString("  if [[ \"$current\" == -* ]]; then\n")
	output.WriteString("    if ((positional == 0)); then COMPREPLY=( $(compgen -W \"$options\" -- \"$current\") ); fi\n")
	output.WriteString("    compopt +o default +o bashdefault 2>/dev/null\n")
	output.WriteString("    return\n")
	output.WriteString("  fi\n")
	output.WriteString("  case \"$command:$subcommand:$positional\" in\n")
	for _, path := range generatedCommandPaths() {
		spec := completionSpecFor(path.Command, path.Subcommand)
		for index, operand := range spec.Operands {
			fmt.Fprintf(&output, "    %s:%s:%d)\n", path.Command, path.Subcommand, index)
			switch operand.Kind {
			case completionValueDirectory:
				output.WriteString("      COMPREPLY=()\n")
				output.WriteString("      while IFS= read -r word; do COMPREPLY+=(\"$word\"); done < <(compgen -d -- \"$current\")\n")
				output.WriteString("      compopt -o filenames 2>/dev/null\n")
			case completionValueZIP:
				output.WriteString("      COMPREPLY=()\n")
				output.WriteString("      while IFS= read -r word; do if [[ -d \"$word\" || \"$word\" == *.zip ]]; then COMPREPLY+=(\"$word\"); fi; done < <(compgen -f -- \"$current\")\n")
				output.WriteString("      compopt -o filenames 2>/dev/null\n")
			case completionValueShell:
				output.WriteString("      COMPREPLY=( $(compgen -W 'bash zsh fish' -- \"$current\") )\n")
			}
			output.WriteString("      compopt +o default +o bashdefault 2>/dev/null\n")
			output.WriteString("      return ;;\n")
		}
	}
	output.WriteString("  esac\n")
	output.WriteString("}\n")
	output.WriteString("complete -o bashdefault -o default -F _rakazo_manager rakazo-manager\n")
	return output.String()
}

func zshCompletion() string {
	var output strings.Builder
	output.WriteString("#compdef rakazo-manager\n")
	for _, command := range generatedCommandSpecs {
		for _, subcommand := range command.Subcommands {
			path := generatedCommandPath{
				Command:     command.Name,
				Subcommand:  subcommand.Name,
				Options:     subcommand.Options,
				OptionLines: subcommand.OptionLines,
			}
			writeZshLeafCompletion(&output, zshCompletionFunctionName(command.Name, subcommand.Name), path)
		}
		path := generatedCommandPath{
			Command:     command.Name,
			Options:     command.Options,
			OptionLines: command.OptionLines,
		}
		if len(command.Subcommands) == 0 {
			writeZshLeafCompletion(&output, zshCompletionFunctionName(command.Name, ""), path)
			continue
		}
		writeZshParentCompletion(&output, command, path)
	}
	output.WriteString("\n_rakazo_manager() {\n")
	output.WriteString("  local context state line\n")
	output.WriteString("  typeset -A opt_args\n")
	output.WriteString("  local -a commands\n")
	output.WriteString("  commands=(\n")
	for _, command := range generatedCommandSpecs {
		fmt.Fprintf(&output, "    %s\n", zshQuote(command.Name+":"+command.Summary))
	}
	output.WriteString("  )\n")
	output.WriteString("  _arguments -C -A '-*' \\\n")
	output.WriteString("    '--yes[confirm guarded operations]' \\\n")
	output.WriteString("    '--no-color[disable ANSI styling]' \\\n")
	output.WriteString("    '(-h --help)-h[show help]' \\\n")
	output.WriteString("    '(-h --help)--help[show help]' \\\n")
	output.WriteString("    '1:command:->command' \\\n")
	output.WriteString("    '*::argument:->arguments'\n")
	output.WriteString("  case \"$state\" in\n")
	output.WriteString("    command) _describe 'command' commands ;;\n")
	output.WriteString("    arguments)\n")
	output.WriteString("      case \"$line[1]\" in\n")
	for _, command := range generatedCommandSpecs {
		fmt.Fprintf(&output, "        %s) %s ;;\n", command.Name, zshCompletionFunctionName(command.Name, ""))
	}
	output.WriteString("        *) _message 'unknown command' ;;\n")
	output.WriteString("      esac ;;\n")
	output.WriteString("  esac\n")
	output.WriteString("}\n")
	output.WriteString("\ncompdef _rakazo_manager rakazo-manager\n")
	return output.String()
}

func writeZshLeafCompletion(output *strings.Builder, functionName string, path generatedCommandPath) {
	output.WriteByte('\n')
	fmt.Fprintf(output, "%s() {\n", functionName)
	output.WriteString("  local context state line\n")
	output.WriteString("  typeset -A opt_args\n")
	specs := zshArgumentSpecs(path)
	if len(specs) == 0 {
		output.WriteString("  _message 'no more arguments'\n")
		output.WriteString("}\n")
		return
	}
	output.WriteString("  _arguments -A '-*' \\\n")
	for index, spec := range specs {
		suffix := " \\\n"
		if index == len(specs)-1 {
			suffix = "\n"
		}
		fmt.Fprintf(output, "    %s%s", zshQuote(spec), suffix)
	}
	output.WriteString("}\n")
}

func writeZshParentCompletion(output *strings.Builder, command generatedCommandSpec, path generatedCommandPath) {
	functionName := zshCompletionFunctionName(command.Name, "")
	output.WriteByte('\n')
	fmt.Fprintf(output, "%s() {\n", functionName)
	output.WriteString("  local context state line\n")
	output.WriteString("  typeset -A opt_args\n")
	output.WriteString("  local -a actions\n")
	output.WriteString("  actions=(\n")
	for _, subcommand := range command.Subcommands {
		fmt.Fprintf(output, "    %s\n", zshQuote(subcommand.Name+":"+subcommand.Summary))
	}
	output.WriteString("  )\n")
	output.WriteString("  _arguments -C -A '-*' \\\n")
	for _, optionSpec := range zshOptionSpecs(path) {
		fmt.Fprintf(output, "    %s \\\n", zshQuote(optionSpec))
	}
	output.WriteString("    '1:action:->action' \\\n")
	output.WriteString("    '*::argument:->arguments'\n")
	output.WriteString("  case \"$state\" in\n")
	output.WriteString("    action)\n")
	output.WriteString("      _describe 'action' actions\n")
	if firstOperandKind(completionSpecFor(command.Name, "")) == completionValueDirectory {
		output.WriteString("      _directories\n")
	}
	output.WriteString("      ;;\n")
	output.WriteString("    arguments)\n")
	output.WriteString("      case \"$line[1]\" in\n")
	for _, subcommand := range command.Subcommands {
		fmt.Fprintf(output, "        %s) %s ;;\n", subcommand.Name, zshCompletionFunctionName(command.Name, subcommand.Name))
	}
	output.WriteString("        *) _message 'no more arguments' ;;\n")
	output.WriteString("      esac ;;\n")
	output.WriteString("  esac\n")
	output.WriteString("}\n")
}

func zshArgumentSpecs(path generatedCommandPath) []string {
	specs := zshOptionSpecs(path)
	for index, operand := range completionSpecFor(path.Command, path.Subcommand).Operands {
		position := index + 1
		switch operand.Kind {
		case completionValueDirectory:
			specs = append(specs, fmt.Sprintf("%d:%s:_directories", position, operand.Name))
		case completionValueZIP:
			specs = append(specs, fmt.Sprintf("%d:%s:_files -g \"*.zip\"", position, operand.Name))
		case completionValueShell:
			specs = append(specs, fmt.Sprintf("%d:%s:(bash zsh fish)", position, operand.Name))
		}
	}
	return specs
}

func zshOptionSpecs(path generatedCommandPath) []string {
	completion := completionSpecFor(path.Command, path.Subcommand)
	specs := make([]string, 0, len(path.Options))
	for _, option := range path.Options {
		description := generatedOptionDescription(path, option)
		kind, takesValue := completion.OptionValues[option]
		if !takesValue {
			specs = append(specs, fmt.Sprintf("%s[%s]", option, description))
			continue
		}
		valueName := strings.TrimPrefix(option, "--")
		action := ""
		if kind == completionValueFile {
			action = "_files"
		} else if choices := completionChoices(kind); len(choices) > 0 {
			action = "(" + strings.Join(choices, " ") + ")"
		}
		specs = append(specs, fmt.Sprintf("%s=[%s]:%s:%s", option, description, valueName, action))
	}
	return specs
}

func fishCompletion() string {
	var output strings.Builder
	output.WriteString("# fish completion for rakazo-manager\n")
	writeFishPositionHelper(&output)
	output.WriteString("complete -c rakazo-manager -n '__fish_use_subcommand' -l yes -d 'Confirm guarded operations'\n")
	output.WriteString("complete -c rakazo-manager -n '__fish_use_subcommand' -l no-color -d 'Disable ANSI styling'\n")
	output.WriteString("complete -c rakazo-manager -n '__fish_use_subcommand' -s h -l help -d 'Show help'\n")
	for _, command := range generatedCommandSpecs {
		fmt.Fprintf(&output, "complete -c rakazo-manager -n __fish_use_subcommand -f -a %s -d %s\n", fishQuote(command.Name), fishQuote(command.Summary))
	}
	for _, command := range generatedCommandSpecs {
		if len(command.Subcommands) > 0 {
			path := generatedCommandPath{Command: command.Name, Options: command.Options, OptionLines: command.OptionLines}
			condition := fishCompletionCondition(path, 0, "operand")
			for _, subcommand := range command.Subcommands {
				fmt.Fprintf(
					&output,
					"complete -c rakazo-manager -n %s -f -a %s -d %s\n",
					fishQuote(condition),
					fishQuote(subcommand.Name),
					fishQuote(subcommand.Summary),
				)
			}
		}
		writeFishPathCompletions(&output, generatedCommandPath{Command: command.Name, Options: command.Options, OptionLines: command.OptionLines})
		for _, subcommand := range command.Subcommands {
			writeFishPathCompletions(&output, generatedCommandPath{
				Command:     command.Name,
				Subcommand:  subcommand.Name,
				Options:     subcommand.Options,
				OptionLines: subcommand.OptionLines,
			})
		}
	}
	return output.String()
}

func writeFishPositionHelper(output *strings.Builder) {
	output.WriteString("function __rakazo_manager_at_position\n")
	output.WriteString("    set -l wanted_command $argv[1]\n")
	output.WriteString("    set -l wanted_subcommand $argv[2]\n")
	output.WriteString("    set -l wanted_index $argv[3]\n")
	output.WriteString("    set -l mode $argv[4]\n")
	output.WriteString("    set -l value_options $argv[5..-1]\n")
	output.WriteString("    set -l tokens (commandline -opc)\n")
	output.WriteString("    set -l command_index 0\n")
	output.WriteString("    for index in (seq 2 (count $tokens))\n")
	output.WriteString("        switch $tokens[$index]\n")
	output.WriteString("            case --yes --no-color -h --help\n")
	output.WriteString("                continue\n")
	output.WriteString("            case '*'\n")
	output.WriteString("                set command_index $index\n")
	output.WriteString("                break\n")
	output.WriteString("        end\n")
	output.WriteString("    end\n")
	output.WriteString("    test $command_index -gt 0; or return 1\n")
	output.WriteString("    test \"$tokens[$command_index]\" = \"$wanted_command\"; or return 1\n")
	output.WriteString("    set -l start (math \"$command_index + 1\")\n")
	output.WriteString("    if test \"$wanted_subcommand\" != -\n")
	output.WriteString("        test (count $tokens) -ge $start; or return 1\n")
	output.WriteString("        test \"$tokens[$start]\" = \"$wanted_subcommand\"; or return 1\n")
	output.WriteString("        set start (math \"$start + 1\")\n")
	output.WriteString("    end\n")
	output.WriteString("    set -l positional 0\n")
	output.WriteString("    set -l skip_next 0\n")
	output.WriteString("    for index in (seq $start (count $tokens))\n")
	output.WriteString("        set -l word $tokens[$index]\n")
	output.WriteString("        if test $skip_next -eq 1\n")
	output.WriteString("            set skip_next 0\n")
	output.WriteString("            continue\n")
	output.WriteString("        end\n")
	output.WriteString("        if string match -qr '^--[^=]+=' -- \"$word\"\n")
	output.WriteString("            continue\n")
	output.WriteString("        end\n")
	output.WriteString("        if string match -qr '^-' -- \"$word\"\n")
	output.WriteString("            if contains -- \"$word\" $value_options\n")
	output.WriteString("                set skip_next 1\n")
	output.WriteString("            end\n")
	output.WriteString("            continue\n")
	output.WriteString("        end\n")
	output.WriteString("        set positional (math \"$positional + 1\")\n")
	output.WriteString("    end\n")
	output.WriteString("    test $positional -eq $wanted_index; or return 1\n")
	output.WriteString("    if test \"$mode\" = operand\n")
	output.WriteString("        test $skip_next -eq 0; or return 1\n")
	output.WriteString("    end\n")
	output.WriteString("end\n")
}

func writeFishPathCompletions(output *strings.Builder, path generatedCommandPath) {
	optionCondition := fishCompletionCondition(path, 0, "option")
	completion := completionSpecFor(path.Command, path.Subcommand)
	for _, option := range path.Options {
		kind, takesValue := completion.OptionValues[option]
		description := generatedOptionDescription(path, option)
		fmt.Fprintf(output, "complete -c rakazo-manager -n %s -l %s", fishQuote(optionCondition), strings.TrimPrefix(option, "--"))
		if takesValue && kind != completionValueBoolean {
			output.WriteString(" -r")
			if kind != completionValueFile {
				output.WriteString(" -f")
			}
			if choices := completionChoices(kind); len(choices) > 0 {
				fmt.Fprintf(output, " -a %s", fishQuote(strings.Join(choices, " ")))
			}
		}
		fmt.Fprintf(output, " -d %s\n", fishQuote(description))
		if kind == completionValueBoolean {
			values := []string{option + "=true", option + "=false"}
			fmt.Fprintf(output, "complete -c rakazo-manager -n %s -f -a %s -d %s\n", fishQuote(optionCondition), fishQuote(strings.Join(values, " ")), fishQuote(description))
		}
	}
	for index, operand := range completion.Operands {
		condition := fishCompletionCondition(path, index, "operand")
		fmt.Fprintf(output, "complete -c rakazo-manager -n %s -f", fishQuote(condition))
		switch operand.Kind {
		case completionValueDirectory:
			output.WriteString(" -a '(__fish_complete_directories)'")
		case completionValueZIP:
			output.WriteString(" -a '(__fish_complete_suffix .zip)'")
		case completionValueShell:
			output.WriteString(" -a 'bash zsh fish'")
		}
		fmt.Fprintf(output, " -d %s\n", fishQuote(operand.Name))
	}
}

func fishCompletionCondition(path generatedCommandPath, operandIndex int, mode string) string {
	var condition string
	if path.Subcommand == "" {
		for _, command := range generatedCommandSpecs {
			if command.Name == path.Command {
				condition = fishTopLevelCondition(command)
				break
			}
		}
	} else {
		condition = "__fish_seen_subcommand_from " + path.Command + "; and __fish_seen_subcommand_from " + path.Subcommand
	}
	parts := []string{"__rakazo_manager_at_position", path.Command}
	if path.Subcommand == "" {
		parts = append(parts, "-")
	} else {
		parts = append(parts, path.Subcommand)
	}
	parts = append(parts, fmt.Sprintf("%d", operandIndex), mode)
	for _, completionOption := range completionOptions(path) {
		if completionOption.Kind != completionValueBoolean {
			parts = append(parts, completionOption.Name)
		}
	}
	return condition + "; and " + strings.Join(parts, " ")
}

func manPage(build BuildInfo) string {
	version := strings.TrimSpace(build.Version)
	if version == "" {
		version = "unknown"
	}
	commit := strings.TrimSpace(build.Commit)
	if commit == "" {
		commit = "unknown"
	}
	date := strings.TrimSpace(build.Date)
	if date == "" {
		date = "unknown"
	}

	var output strings.Builder
	fmt.Fprintf(
		&output,
		".TH RAKAZO\\-MANAGER 1 \"%s\" \"Rakazo Manager %s\" \"Rakazo Manager Manual\"\n",
		roffHeaderEscape(manHeaderDate(date)),
		roffHeaderEscape(version),
	)
	output.WriteString(".SH NAME\nrakazo\\-manager \\- install and manage Rakazo with Docker Compose\n")
	output.WriteString(".SH SYNOPSIS\n.B rakazo\\-manager\n.RI [ global-options ] \\ command \\ [ command-options ] \\ [ folder ]\n")
	output.WriteString(".SH DESCRIPTION\n")
	writeRoffText(&output, "Rakazo Manager is a small terminal application for creating and operating durable Rakazo instances with Docker Compose. Instance data remains in host bind mounts.")
	output.WriteString(".SH GLOBAL OPTIONS\n")
	writeRoffOption(&output, "--yes", "Confirm guarded operations without prompting.")
	writeRoffOption(&output, "--no-color", "Disable ANSI styling. NO_COLOR and TERM=dumb are also respected.")
	writeRoffOption(&output, "-h, --help", "Show help.")
	output.WriteString(".SH COMMANDS\n")
	for _, command := range generatedCommandSpecs {
		usage := command.Name
		if command.Usage != "" {
			usage += " " + command.Usage
		}
		writeRoffOption(&output, usage, command.Summary+".")
		for _, subcommand := range command.Subcommands {
			writeRoffOption(&output, command.Name+" "+subcommand.Usage, subcommand.Summary+".")
		}
	}
	for _, command := range generatedCommandSpecs {
		if len(command.OptionLines) > 0 {
			fmt.Fprintf(&output, ".SS %s OPTIONS\n", strings.ToUpper(roffEscape(command.Name)))
			for _, option := range command.OptionLines {
				writeRoffOption(&output, option.Usage, option.Description)
			}
		}
		for _, subcommand := range command.Subcommands {
			if len(subcommand.OptionLines) == 0 {
				continue
			}
			fmt.Fprintf(&output, ".SS %s %s OPTIONS\n", strings.ToUpper(roffEscape(command.Name)), strings.ToUpper(roffEscape(subcommand.Name)))
			for _, option := range subcommand.OptionLines {
				writeRoffOption(&output, option.Usage, option.Description)
			}
		}
	}
	output.WriteString(".SH FILES\n")
	writeRoffOption(&output, "FOLDER/docker-compose.yml", "Managed base Compose file.")
	writeRoffOption(&output, "FOLDER/docker-compose.override.yml", "Optional user-owned Compose overrides.")
	writeRoffOption(&output, "FOLDER/.manager/", "Manager metadata, state, credentials, and operation history.")
	writeRoffOption(&output, "FOLDER/pg/", "Postgres data, bind-mounted at /var/lib/postgresql/data.")
	writeRoffOption(&output, "FOLDER/data/", "Bot homes and artifacts, bind-mounted at /data.")
	writeRoffOption(&output, "FOLDER/workspace/", "Optional project files mounted at /workspace.")
	writeRoffOption(&output, "FOLDER/backups/", "Backup and recovery archives.")
	output.WriteString(".SH ENVIRONMENT\n")
	writeRoffOption(&output, "NO_COLOR", "Disable ANSI styling when nonempty.")
	writeRoffOption(&output, "TERM", "The value dumb disables ANSI styling.")
	writeRoffOption(&output, "RAKAZO_MANAGER_REPOSITORY", "Override the GitHub repository used by self-update.")
	output.WriteString(".SH VERSION\n")
	writeRoffText(&output, fmt.Sprintf("Version %s, commit %s, built %s.", version, commit, date))
	output.WriteString(".SH SEE ALSO\n.BR docker (1)\n")
	return output.String()
}

func writeRoffOption(output *strings.Builder, usage, description string) {
	output.WriteString(".TP\n.B ")
	output.WriteString(roffEscape(usage))
	output.WriteByte('\n')
	writeRoffText(output, description)
}

func writeRoffText(output *strings.Builder, value string) {
	const width = 72
	column := 0
	for _, word := range strings.Fields(roffEscape(value)) {
		if column > 0 && column+1+len(word) > width {
			output.WriteByte('\n')
			column = 0
		}
		if column > 0 {
			output.WriteByte(' ')
			column++
		}
		if column == 0 && (strings.HasPrefix(word, ".") || strings.HasPrefix(word, "'")) {
			output.WriteString(`\&`)
			column += 2
		}
		output.WriteString(word)
		column += len(word)
	}
	output.WriteByte('\n')
}

func roffEscape(value string) string {
	var output strings.Builder
	atLineStart := true
	for _, character := range value {
		switch {
		case character == '\n' || character == '\r' || character == '\t':
			output.WriteByte(' ')
			atLineStart = false
		case unicode.IsControl(character):
			output.WriteByte(' ')
			atLineStart = false
		case character == '\\':
			output.WriteString(`\e`)
			atLineStart = false
		case character == '-':
			output.WriteString(`\-`)
			atLineStart = false
		case character == '"':
			output.WriteString(`\(dq`)
			atLineStart = false
		case atLineStart && (character == '.' || character == '\''):
			output.WriteString(`\&`)
			output.WriteRune(character)
			atLineStart = false
		default:
			output.WriteRune(character)
			atLineStart = false
		}
	}
	return output.String()
}

func roffHeaderEscape(value string) string {
	var output strings.Builder
	for _, character := range value {
		switch {
		case character == '\n' || character == '\r' || character == '\t':
			output.WriteByte(' ')
		case unicode.IsControl(character):
			output.WriteByte(' ')
		case character == '\\':
			output.WriteString(`\e`)
		case character == '"':
			output.WriteString(`\(dq`)
		default:
			output.WriteRune(character)
		}
	}
	return output.String()
}

func manHeaderDate(value string) string {
	if len(value) >= len("2006-01-02") &&
		value[4] == '-' && value[7] == '-' &&
		allASCIIDigits(value[0:4]) &&
		allASCIIDigits(value[5:7]) &&
		allASCIIDigits(value[8:10]) {
		return value[:10]
	}
	return value
}

func allASCIIDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

type generatedCommandPath struct {
	Command     string
	Subcommand  string
	Options     []string
	OptionLines []generatedOptionSpec
}

type generatedCompletionOption struct {
	Name string
	Kind generatedCompletionValueKind
}

func generatedCommandPaths() []generatedCommandPath {
	paths := make([]generatedCommandPath, 0, len(generatedCommandSpecs))
	for _, command := range generatedCommandSpecs {
		paths = append(paths, generatedCommandPath{
			Command:     command.Name,
			Options:     command.Options,
			OptionLines: command.OptionLines,
		})
		for _, subcommand := range command.Subcommands {
			paths = append(paths, generatedCommandPath{
				Command:     command.Name,
				Subcommand:  subcommand.Name,
				Options:     subcommand.Options,
				OptionLines: subcommand.OptionLines,
			})
		}
	}
	return paths
}

func completionPath(command, subcommand string) string {
	if subcommand == "" {
		return command
	}
	return command + " " + subcommand
}

func completionSpecFor(command, subcommand string) generatedCompletionSpec {
	return generatedCompletionSpecs[completionPath(command, subcommand)]
}

func completionOptions(path generatedCommandPath) []generatedCompletionOption {
	values := completionSpecFor(path.Command, path.Subcommand).OptionValues
	options := make([]generatedCompletionOption, 0, len(values))
	for _, name := range path.Options {
		if kind, ok := values[name]; ok {
			options = append(options, generatedCompletionOption{Name: name, Kind: kind})
		}
	}
	return options
}

func completionChoices(kind generatedCompletionValueKind) []string {
	switch kind {
	case completionValueBoolean:
		return []string{"true", "false"}
	case completionValueBindAddress:
		return []string{"127.0.0.1", "0.0.0.0"}
	default:
		return nil
	}
}

func firstOperandKind(spec generatedCompletionSpec) generatedCompletionValueKind {
	if len(spec.Operands) == 0 {
		return ""
	}
	return spec.Operands[0].Kind
}

func zshCompletionFunctionName(command, subcommand string) string {
	name := completionPath(command, subcommand)
	name = strings.NewReplacer("-", "_", " ", "_").Replace(name)
	return "_rakazo_manager_" + name
}

func generatedOptionDescription(path generatedCommandPath, option string) string {
	for _, line := range path.OptionLines {
		fields := strings.Fields(line.Usage)
		if len(fields) > 0 && (fields[0] == option || strings.HasPrefix(fields[0], option+"=")) {
			return strings.TrimSuffix(line.Description, ".")
		}
	}
	return strings.TrimPrefix(option, "--")
}

func generatedCommandNames() string {
	names := make([]string, 0, len(generatedCommandSpecs))
	for _, command := range generatedCommandSpecs {
		names = append(names, command.Name)
	}
	return strings.Join(names, " ")
}

func generatedSubcommandNames(command generatedCommandSpec) string {
	names := make([]string, 0, len(command.Subcommands))
	for _, subcommand := range command.Subcommands {
		names = append(names, subcommand.Name)
	}
	return strings.Join(names, " ")
}

func fishTopLevelCondition(command generatedCommandSpec) string {
	condition := "__fish_seen_subcommand_from " + command.Name
	for _, subcommand := range command.Subcommands {
		condition += "; and not __fish_seen_subcommand_from " + subcommand.Name
	}
	return condition
}

func bashQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func zshQuote(value string) string {
	return bashQuote(value)
}

func fishQuote(value string) string {
	return bashQuote(value)
}

func writeGeneratedOutput(output io.Writer, kind, content string) error {
	if output == nil {
		return fmt.Errorf("write %s: output is not configured", kind)
	}
	written, err := io.WriteString(output, content)
	if err != nil {
		return fmt.Errorf("write %s: %w", kind, err)
	}
	if written != len(content) {
		return fmt.Errorf("write %s: %w", kind, io.ErrShortWrite)
	}
	return nil
}
