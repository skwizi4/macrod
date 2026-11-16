// Package presets holds ready-made configs (ТЗ §21.3), copied by
// macroctl init. They are written for the repository root: relative paths
// such as prompts/classify.md start there.
package presets

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

//go:embed *.yaml
var files embed.FS

// Names are the available presets, sorted.
func Names() []string {
	entries, err := fs.Glob(files, "*.yaml")
	if err != nil {
		panic(err) // the pattern is constant
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = strings.TrimSuffix(e, ".yaml")
	}
	slices.Sort(names)
	return names
}

// Read returns the config file of a preset.
func Read(name string) ([]byte, error) {
	if !slices.Contains(Names(), name) {
		return nil, fmt.Errorf("unknown preset %q (want one of %s)", name, strings.Join(Names(), ", "))
	}
	return files.ReadFile(name + ".yaml")
}
