package export

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"macro-collector/internal/config"
)

// InstructionsFile is the name of an agent's prompt in its export folder (§21.5).
const InstructionsFile = "instructions.md"

// Prompt is an agent's prompt file prompts/<region>/<agent>.md: YAML front
// matter with a version, then the Markdown body (§21.5).
type Prompt struct {
	Name    string // <region>/<agent>
	Version string // "<version> (<first 7 hex of sha256 of the file>)"
	Body    string // the file after its front matter
}

// LoadPrompt reads and parses a prompt file; name is <region>/<agent>.
func LoadPrompt(path, name string) (Prompt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s: %w", name, err)
	}
	p, err := ParsePrompt(raw, name)
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s (%s): %w", name, path, err)
	}
	return p, nil
}

// ParsePrompt parses a prompt file. The front matter opens with a line "---"
// on the first line and closes with the next line that is exactly "---";
// later "---" lines (Markdown rules) belong to the body.
func ParsePrompt(raw []byte, name string) (Prompt, error) {
	sum := sha256.Sum256(raw)
	text := strings.ReplaceAll(string(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))), "\r\n", "\n")
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\n") != "---" {
		return Prompt{}, fmt.Errorf("no front matter: the first line must be ---")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\n") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Prompt{}, fmt.Errorf("front matter is not closed with ---")
	}
	version, err := promptVersion(strings.Join(lines[1:end], ""))
	if err != nil {
		return Prompt{}, err
	}
	body := strings.TrimLeft(strings.Join(lines[end+1:], ""), "\n")
	if strings.TrimSpace(body) == "" {
		return Prompt{}, fmt.Errorf("empty body")
	}
	return Prompt{Name: name, Version: version + " (" + hex.EncodeToString(sum[:])[:7] + ")", Body: body}, nil
}

// promptVersion reads the front matter: only the key version, a non-empty
// scalar taken as written (1.10 stays 1.10, it is not a number).
func promptVersion(front string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return "", fmt.Errorf("front matter: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("front matter: want a mapping with version")
	}
	m := doc.Content[0]
	version := ""
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		if k.Value != "version" {
			return "", fmt.Errorf("front matter: unknown key %q", k.Value)
		}
		if v.Kind != yaml.ScalarNode || strings.TrimSpace(v.Value) == "" {
			return "", fmt.Errorf("front matter: version must be a non-empty scalar")
		}
		version = strings.TrimSpace(v.Value)
	}
	if version == "" {
		return "", fmt.Errorf("front matter: no version")
	}
	return version, nil
}

// Render is the text of instructions.md: a header naming the prompt and its
// version, which the agent copies into its report, then the body.
func (p Prompt) Render() string {
	return "---\nprompt: " + p.Name + "\nprompt_version: " + strconv.Quote(p.Version) + "\n---\n\n" + p.Body
}

// CheckPrompts loads the prompt of every agent of every region: n is how many
// parsed, err joins every missing or malformed one (macroctl doctor, tests).
func CheckPrompts(cfg *config.Config) (n int, err error) {
	var errs []error
	for i := range cfg.Regions {
		r := &cfg.Regions[i]
		for j := range r.Agents {
			a := &r.Agents[j]
			if _, err := LoadPrompt(cfg.PromptPath(r, a), r.ID+"/"+a.ID); err != nil {
				errs = append(errs, err)
				continue
			}
			n++
		}
	}
	return n, errors.Join(errs...)
}
