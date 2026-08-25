package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type Palette struct {
	Accent string
	Good   string
	Warn   string
	Bad    string
	Muted  string
	Bold   string
	Reset  string
}

type UI struct {
	In        *bufio.Reader
	Out       io.Writer
	Err       io.Writer
	Palette   Palette
	AssumeYes bool
}

type Item struct {
	Key         string
	Label       string
	Description string
}

func New(in io.Reader, out, errOut io.Writer, color, assumeYes bool) *UI {
	palette := Palette{}
	if color {
		palette = Palette{
			Accent: "\x1b[38;5;81m",
			Good:   "\x1b[38;5;78m",
			Warn:   "\x1b[38;5;220m",
			Bad:    "\x1b[38;5;203m",
			Muted:  "\x1b[38;5;245m",
			Bold:   "\x1b[1m",
			Reset:  "\x1b[0m",
		}
	}
	return &UI{
		In:        bufio.NewReader(in),
		Out:       out,
		Err:       errOut,
		Palette:   palette,
		AssumeYes: assumeYes,
	}
}

func ColorEnabled(out *os.File, disabled bool) bool {
	if disabled || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := out.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (ui *UI) Banner(version, root string) {
	p := ui.Palette
	displayVersion := "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
	fmt.Fprintf(ui.Out, "\n%s╭────────────────────────────────────────────────────────────╮%s\n", p.Accent, p.Reset)
	fmt.Fprintf(ui.Out, "%s│%s %sRakazo Manager%s  %-39s%s│%s\n", p.Accent, p.Reset, p.Bold, p.Reset, displayVersion, p.Accent, p.Reset)
	fmt.Fprintf(ui.Out, "%s│%s %-58s %s│%s\n", p.Accent, p.Reset, truncate("Stack  "+root, 58), p.Accent, p.Reset)
	fmt.Fprintf(ui.Out, "%s╰────────────────────────────────────────────────────────────╯%s\n", p.Accent, p.Reset)
}

func (ui *UI) Section(title string) {
	p := ui.Palette
	fmt.Fprintf(ui.Out, "\n%s%s%s\n", p.Bold, title, p.Reset)
	fmt.Fprintf(ui.Out, "%s%s%s\n", p.Muted, strings.Repeat("─", min(60, max(12, len(title)+8))), p.Reset)
}

func (ui *UI) Menu(title string, items []Item) (string, error) {
	ui.Section(title)
	for _, item := range items {
		fmt.Fprintf(ui.Out, "  %s%s%s  %s%s%s\n", ui.Palette.Accent, item.Key, ui.Palette.Reset, ui.Palette.Bold, item.Label, ui.Palette.Reset)
		if item.Description != "" {
			fmt.Fprintf(ui.Out, "     %s%s%s\n", ui.Palette.Muted, item.Description, ui.Palette.Reset)
		}
	}
	return ui.Prompt("Select", "")
}

func (ui *UI) Prompt(label, defaultValue string) (string, error) {
	if defaultValue != "" {
		fmt.Fprintf(ui.Out, "%s%s%s [%s]: ", ui.Palette.Accent, label, ui.Palette.Reset, defaultValue)
	} else {
		fmt.Fprintf(ui.Out, "%s%s%s: ", ui.Palette.Accent, label, ui.Palette.Reset)
	}
	line, err := ui.In.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func (ui *UI) PromptInt(label string, defaultValue int) (int, error) {
	value, err := ui.Prompt(label, strconv.Itoa(defaultValue))
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", label)
	}
	return parsed, nil
}

func (ui *UI) Confirm(message string, defaultYes bool) (bool, error) {
	if ui.AssumeYes {
		return true, nil
	}
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	value, err := ui.Prompt(message+" "+suffix, "")
	if err != nil {
		return false, err
	}
	if value == "" {
		return defaultYes, nil
	}
	switch strings.ToLower(value) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("answer yes or no")
	}
}

func (ui *UI) RequirePhrase(message, phrase string) error {
	if ui.AssumeYes {
		return nil
	}
	ui.Warn("%s", message)
	value, err := ui.Prompt("Type "+phrase+" to continue", "")
	if err != nil {
		return err
	}
	if value != phrase {
		return fmt.Errorf("confirmation did not match; operation cancelled")
	}
	return nil
}

func (ui *UI) Info(format string, args ...any) {
	fmt.Fprintf(ui.Out, "%s•%s %s\n", ui.Palette.Accent, ui.Palette.Reset, fmt.Sprintf(format, args...))
}

func (ui *UI) Step(format string, args ...any) {
	fmt.Fprintf(ui.Out, "%s→%s %s\n", ui.Palette.Accent, ui.Palette.Reset, fmt.Sprintf(format, args...))
}

func (ui *UI) Success(format string, args ...any) {
	fmt.Fprintf(ui.Out, "%s✓%s %s\n", ui.Palette.Good, ui.Palette.Reset, fmt.Sprintf(format, args...))
}

func (ui *UI) Warn(format string, args ...any) {
	fmt.Fprintf(ui.Err, "%s!%s %s\n", ui.Palette.Warn, ui.Palette.Reset, fmt.Sprintf(format, args...))
}

func (ui *UI) Failure(format string, args ...any) {
	fmt.Fprintf(ui.Err, "%s×%s %s\n", ui.Palette.Bad, ui.Palette.Reset, fmt.Sprintf(format, args...))
}

func (ui *UI) KeyValue(key string, value any) {
	fmt.Fprintf(ui.Out, "  %s%-20s%s %v\n", ui.Palette.Muted, key, ui.Palette.Reset, value)
}

func (ui *UI) Pause() {
	_, _ = ui.Prompt("Press Enter to continue", "")
}

func truncate(value string, length int) string {
	runes := []rune(value)
	if len(runes) <= length {
		return value
	}
	if length < 2 {
		return string(runes[:length])
	}
	return string(runes[:length-1]) + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
