package classify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Prompt is prompts/classify.md without its leading HTML comment: the
// system instruction sent with every Gemini request.
type Prompt struct {
	Text string
	Hash string // sha256 of Text; cache entries of another prompt are stale
}

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	promptTag   = regexp.MustCompile("(?m)^- `([a-z_]+)` —")
)

// LoadPrompt reads the prompt file and checks that its "### Tags" list is
// exactly want (classify.tags), so the model and the keyword fallback use the same
// set (LOOKUP M6: section names inside the comment are ignored).
func LoadPrompt(path string, classifyTags []string) (Prompt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Prompt{}, fmt.Errorf("prompt %s: %w", path, err)
	}
	text := strings.TrimSpace(htmlComment.ReplaceAllString(string(raw), ""))
	for _, sec := range []string{"## System", "### Tags", "## Input", "## Output"} {
		if !strings.Contains(text, sec+"\n") {
			return Prompt{}, fmt.Errorf("prompt %s: no %q section", path, sec)
		}
	}
	tagsSection := text[strings.Index(text, "### Tags\n"):]
	if i := strings.Index(tagsSection[len("### Tags\n"):], "\n#"); i >= 0 {
		tagsSection = tagsSection[:len("### Tags\n")+i]
	}
	var tags []string
	for _, m := range promptTag.FindAllStringSubmatch(tagsSection, -1) {
		tags = append(tags, m[1])
	}
	got, want := slices.Sorted(slices.Values(tags)), slices.Sorted(slices.Values(classifyTags))
	if !slices.Equal(got, want) {
		return Prompt{}, fmt.Errorf("prompt %s: tags %v differ from classify tags %v", path, tags, classifyTags)
	}
	sum := sha256.Sum256([]byte(text))
	return Prompt{Text: text, Hash: hex.EncodeToString(sum[:])}, nil
}
