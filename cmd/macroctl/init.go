package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"

	"macro-collector/presets"
)

// init runs before any config exists, so it is not in the commands table.
const (
	initCommand = "init"
	initUsage   = "init --preset <name> [--force]   copy a preset to the config file"
)

// runInit copies a preset to the config file; an existing file is kept
// unless force is set.
func runInit(path string, args []string) error {
	flags := flag.NewFlagSet(initCommand, flag.ExitOnError)
	preset := flags.String("preset", "", "preset name")
	force := flags.Bool("force", false, "overwrite an existing config file")
	_ = flags.Parse(args)
	if *preset == "" {
		return fmt.Errorf("--preset is required (one of %v)", presets.Names())
	}
	body, err := presets.Read(*preset)
	if err != nil {
		return err
	}
	if !*force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (--force overwrites it)", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("check %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Printf("%s: preset %s written; put API keys into .env, then run macroctl doctor\n", path, *preset)
	return nil
}
