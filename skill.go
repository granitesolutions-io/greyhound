package greyhound

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// Skill represents a loaded skill with metadata and prompt template.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Output      string `json:"output"`
	Prompt      string // loaded from .md file, not in JSON
}

// LoadSkill loads a skill's metadata (.json) and prompt template (.md) from the
// caller's embedded filesystem. The dir parameter is the directory within the
// embed.FS that contains the skill files (e.g. "skills").
func LoadSkill(fs embed.FS, dir string, name string) (*Skill, error) {
	jsonPath := fmt.Sprintf("%s/%s.json", dir, name)
	jsonData, err := fs.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("skill %q not found: %w", name, err)
	}

	var skill Skill
	if err := json.Unmarshal(jsonData, &skill); err != nil {
		return nil, fmt.Errorf("invalid skill metadata %q: %w", name, err)
	}

	mdPath := fmt.Sprintf("%s/%s.md", dir, name)
	mdData, err := fs.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("skill prompt %q not found: %w", name, err)
	}
	skill.Prompt = string(mdData)

	return &skill, nil
}

// ListSkills returns the names of all available skills in the given directory
// of the embedded filesystem.
func ListSkills(fs embed.FS, dir string) []string {
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	return names
}

// RenderPrompt renders the skill's Go text/template with the provided data struct.
func RenderPrompt(skill *Skill, data interface{}) (string, error) {
	tmpl, err := template.New(skill.Name).Parse(skill.Prompt)
	if err != nil {
		return "", fmt.Errorf("invalid skill template %q: %w", skill.Name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering skill template %q: %w", skill.Name, err)
	}

	return buf.String(), nil
}
