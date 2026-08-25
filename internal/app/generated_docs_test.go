package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionScriptsAreDeterministicAndComplete(t *testing.T) {
	tests := []struct {
		shell   string
		marker  string
		command string
		args    []string
	}{
		{shell: "bash", marker: "complete -o bashdefault -o default -F _rakazo_manager rakazo-manager", command: "bash", args: []string{"-n"}},
		{shell: "zsh", marker: "compdef _rakazo_manager rakazo-manager", command: "zsh", args: []string{"-n"}},
		{shell: "fish", marker: "complete -c rakazo-manager", command: "fish", args: []string{"--no-execute"}},
	}

	for _, test := range tests {
		t.Run(test.shell, func(t *testing.T) {
			first, err := completionScript(test.shell)
			if err != nil {
				t.Fatal(err)
			}
			second, err := completionScript(test.shell)
			if err != nil {
				t.Fatal(err)
			}
			if first != second {
				t.Fatal("completion output is not deterministic")
			}
			if !strings.HasSuffix(first, "\n") {
				t.Fatal("completion must end with a newline")
			}
			if !strings.Contains(first, test.marker) {
				t.Fatalf("missing shell registration marker %q", test.marker)
			}
			if strings.Contains(first, "\x1b") {
				t.Fatal("completion contains an ANSI escape")
			}
			for _, command := range generatedCommandSpecs {
				if !strings.Contains(first, command.Name) {
					t.Errorf("completion is missing command %q", command.Name)
				}
				for _, option := range command.Options {
					if !completionContainsOption(first, test.shell, option) {
						t.Errorf("completion is missing %s option %q", command.Name, option)
					}
				}
				for _, subcommand := range command.Subcommands {
					if !strings.Contains(first, subcommand.Name) {
						t.Errorf("completion is missing subcommand %q for %s", subcommand.Name, command.Name)
					}
					for _, option := range subcommand.Options {
						if !completionContainsOption(first, test.shell, option) {
							t.Errorf("completion is missing %s %s option %q", command.Name, subcommand.Name, option)
						}
					}
				}
			}
			validateWithInstalledParser(t, test.command, test.args, first)
		})
	}
}

func completionContainsOption(script, shell, option string) bool {
	if shell == "fish" {
		return strings.Contains(script, "-l "+strings.TrimPrefix(option, "--"))
	}
	return strings.Contains(script, option)
}

func TestCompletionRejectsUnsupportedShellWithoutOutput(t *testing.T) {
	if script, err := completionScript("powershell"); err == nil {
		t.Fatalf("expected unsupported-shell error, got script %q", script)
	} else if script != "" {
		t.Fatalf("unsupported shell returned output %q", script)
	}

	var output bytes.Buffer
	app := &App{Out: &output}
	if err := app.completionCommand([]string{"powershell"}); err == nil {
		t.Fatal("expected command error")
	}
	if output.Len() != 0 {
		t.Fatalf("failed command wrote stdout: %q", output.String())
	}
	if err := app.completionCommand(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestCompletionMetadataMatchesDocumentedOptions(t *testing.T) {
	paths := map[string]generatedCommandPath{}
	for _, path := range generatedCommandPaths() {
		paths[completionPath(path.Command, path.Subcommand)] = path
	}
	for name, completion := range generatedCompletionSpecs {
		path, ok := paths[name]
		if !ok {
			t.Errorf("completion metadata references unknown command path %q", name)
			continue
		}
		knownOptions := map[string]bool{}
		for _, option := range path.Options {
			knownOptions[option] = true
		}
		for option := range completion.OptionValues {
			if !knownOptions[option] {
				t.Errorf("completion metadata for %s references undocumented option %s", name, option)
			}
		}
	}
	for name, path := range paths {
		completion := generatedCompletionSpecs[name]
		for _, option := range path.OptionLines {
			fields := strings.Fields(option.Usage)
			if len(fields) == 0 {
				t.Fatalf("empty option usage for %s", name)
			}
			optionName := strings.SplitN(fields[0], "=", 2)[0]
			takesValue := len(fields) > 1 || strings.Contains(fields[0], "=")
			_, modeled := completion.OptionValues[optionName]
			if takesValue != modeled {
				t.Errorf("completion value metadata for %s %s: modeled=%t takes-value=%t", name, optionName, modeled, takesValue)
			}
		}
	}
}

func TestCompletionScriptsMatchFlagParserPositionAndValueSemantics(t *testing.T) {
	bash := bashCompletion()
	for _, forbidden := range []string{
		"options='--yes",
		"options=\"--yes",
	} {
		if strings.Contains(bash, forbidden) {
			t.Errorf("bash offers global options after command via %q", forbidden)
		}
	}
	for _, required := range []string{
		"config:set:--bind-address=*",
		"config:set:--rebuild-on-start=*",
		"compgen -W 'true false'",
		"compgen -d",
		"*.zip",
		"compgen -W 'bash zsh fish'",
	} {
		if !strings.Contains(bash, required) {
			t.Errorf("bash completion is missing typed behavior %q", required)
		}
	}

	zsh := zshCompletion()
	for _, required := range []string{
		"_arguments -C -A '-*'",
		"1:folder:_directories",
		"1:backup:_files -g \"*.zip\"",
		"--bind-address=[Set the host bind address]:bind-address:(127.0.0.1 0.0.0.0)",
		"--rebuild-on-start=[Enable or disable base Compose regeneration on start]:rebuild-on-start:(true false)",
		"1:shell:(bash zsh fish)",
	} {
		if !strings.Contains(zsh, required) {
			t.Errorf("zsh completion is missing typed behavior %q", required)
		}
	}

	fish := fishCompletion()
	for _, global := range []string{"-l yes", "-l no-color", "-s h -l help"} {
		line := completionLineContaining(t, fish, global)
		if !strings.Contains(line, "-n '__fish_use_subcommand'") {
			t.Errorf("fish global option is not limited to the pre-command position: %s", line)
		}
	}
	for _, required := range []string{
		"__fish_complete_directories",
		"__fish_complete_suffix .zip",
		"-l bind-address -r -f -a '127.0.0.1 0.0.0.0'",
		"-a '--rebuild-on-start=true --rebuild-on-start=false'",
		"-a 'bash zsh fish'",
	} {
		if !strings.Contains(fish, required) {
			t.Errorf("fish completion is missing typed behavior %q", required)
		}
	}
}

func TestBashCompletionBehavior(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	target := filepath.Join(t.TempDir(), "managed-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "recovery.zip")
	if err := os.WriteFile(archive, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := bashCompletion() + `
target=$1
archive=$2
COMP_WORDS=(rakazo-manager install --)
COMP_CWORD=2
COMPREPLY=()
_rakazo_manager
[[ " ${COMPREPLY[*]} " == *" --dry-run "* ]]
[[ " ${COMPREPLY[*]} " != *" --yes "* ]]
[[ " ${COMPREPLY[*]} " != *" --no-color "* ]]
COMP_WORDS=(rakazo-manager config set --bind-address "")
COMP_CWORD=4
COMPREPLY=()
_rakazo_manager
[[ " ${COMPREPLY[*]} " == *" 127.0.0.1 "* ]]
[[ " ${COMPREPLY[*]} " == *" 0.0.0.0 "* ]]
COMP_WORDS=(rakazo-manager config set --rebuild-on-start=)
COMP_CWORD=3
COMPREPLY=()
_rakazo_manager
[[ " ${COMPREPLY[*]} " == *" --rebuild-on-start=true "* ]]
[[ " ${COMPREPLY[*]} " == *" --rebuild-on-start=false "* ]]
COMP_WORDS=(rakazo-manager completion "")
COMP_CWORD=2
COMPREPLY=()
_rakazo_manager
[[ " ${COMPREPLY[*]} " == " bash zsh fish " ]]
COMP_WORDS=(rakazo-manager install "${target%?}")
COMP_CWORD=2
COMPREPLY=()
_rakazo_manager
[[ " ${COMPREPLY[*]} " == *" $target "* ]]
COMP_WORDS=(rakazo-manager restore "${archive%?}")
COMP_CWORD=2
COMPREPLY=()
_rakazo_manager
[[ " ${COMPREPLY[*]} " == *" $archive "* ]]
COMP_WORDS=(rakazo-manager install "$target" --)
COMP_CWORD=3
COMPREPLY=()
_rakazo_manager
[[ ${#COMPREPLY[@]} -eq 0 ]]
`
	command := exec.Command(bashPath, "-c", script, "bash", target, archive)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("bash completion behavior failed: %v\n%s", err, output.String())
	}
}

func completionLineContaining(t *testing.T, script, marker string) string {
	t.Helper()
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("completion script is missing %q", marker)
	return ""
}

func TestManPageHasValidStructureAndEscapesBuildInformation(t *testing.T) {
	build := BuildInfo{
		Version: "v1.2.3\"\n.SH INJECTED\x1b",
		Commit:  "abc\\def",
		Date:    ".2026-08-11",
	}
	first := manPage(build)
	second := manPage(build)
	if first != second {
		t.Fatal("manual output is not deterministic")
	}
	if strings.Contains(first, "\x1b") {
		t.Fatal("manual contains an ANSI escape")
	}
	if strings.Contains(first, "\n.SH INJECTED") {
		t.Fatal("build metadata injected a roff macro")
	}
	for _, required := range []string{
		".TH RAKAZO\\-MANAGER 1",
		".SH NAME",
		".SH SYNOPSIS",
		".SH GLOBAL OPTIONS",
		".SH COMMANDS",
		".SH FILES",
		".SH ENVIRONMENT",
		".SH VERSION",
		".SH SEE ALSO",
		"completion",
		"instances list",
		"config show",
		"config set",
		"import\\-instance",
		"update check",
		"schedule set",
		"schedule show",
		"schedule remove",
		"schedule run",
		"decommission",
		"\\-\\-dry\\-run",
		"\\-\\-json",
		"self\\-update",
		"docker\\-compose.override.yml",
	} {
		if !strings.Contains(first, required) {
			t.Errorf("manual is missing %q", required)
		}
	}
	if !strings.HasSuffix(first, "\n") {
		t.Fatal("manual must end with a newline")
	}
	assertKnownRoffMacros(t, first)
	validateWithInstalledParser(t, "mandoc", []string{"-Tlint"}, manPage(BuildInfo{
		Version: "v1.2.3",
		Commit:  "abc123",
		Date:    "2026-08-11",
	}))
}

func TestGeneratedCommandsWriteOnlyStandardOutput(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := &App{
		Build: BuildInfo{Version: "v1.0.0", Commit: "abc123", Date: "2026-08-11T12:00:00Z"},
		Out:   &output,
		Err:   &errorOutput,
	}
	if err := app.completionCommand([]string{"bash"}); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("completion command wrote no output")
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("completion command wrote stderr: %q", errorOutput.String())
	}

	output.Reset()
	if err := app.manCommand(nil); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("man command wrote no output")
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("man command wrote stderr: %q", errorOutput.String())
	}
	if err := app.manCommand([]string{"unexpected"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func assertKnownRoffMacros(t *testing.T, document string) {
	t.Helper()
	known := map[string]bool{
		".B":  true,
		".BR": true,
		".RI": true,
		".SH": true,
		".SS": true,
		".TH": true,
		".TP": true,
	}
	for lineNumber, line := range strings.Split(document, "\n") {
		if !strings.HasPrefix(line, ".") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || !known[fields[0]] {
			t.Errorf("line %d uses unsupported roff macro: %q", lineNumber+1, line)
		}
	}
}

func validateWithInstalledParser(t *testing.T, name string, args []string, input string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Logf("%s is unavailable; structural assertions still cover generated content", name)
		return
	}
	command := exec.Command(path, args...)
	command.Stdin = strings.NewReader(input)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("%s syntax validation failed: %v\n%s", name, err, output.String())
	}
}
