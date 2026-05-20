package greyhound

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FileEntry represents a file to be written (used by "files" output type).
type FileEntry struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// ParseJSON extracts the first JSON object {...} from text and unmarshals it to T.
func ParseJSON[T any](text string) (*T, error) {
	text = stripCodeFences(text)
	text = strings.TrimSpace(text)

	start := strings.Index(text, "{")
	if start == -1 {
		return nil, fmt.Errorf("no JSON object found in output")
	}

	end := findMatchingBrace(text, start)
	if end == -1 {
		return nil, fmt.Errorf("no matching } found in output")
	}

	var v T
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return &v, nil
}

// ParseJSONArray extracts the first JSON array [...] from text and unmarshals it to []T.
func ParseJSONArray[T any](text string) ([]T, error) {
	text = stripCodeFences(text)
	text = strings.TrimSpace(text)

	start := strings.Index(text, "[")
	if start == -1 {
		return nil, fmt.Errorf("no JSON array found in output")
	}

	end := findMatchingBracket(text, start)
	if end == -1 {
		return nil, fmt.Errorf("no matching ] found in output")
	}

	var v []T
	if err := json.Unmarshal([]byte(text[start:end+1]), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return v, nil
}

// ParseFiles is a convenience wrapper for ParseJSONArray[FileEntry].
func ParseFiles(text string) ([]FileEntry, error) {
	return ParseJSONArray[FileEntry](text)
}

// stripCodeFences removes markdown code fences (```json ... ``` or ``` ... ```) from text.
func stripCodeFences(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// findMatchingBracket finds the closing ] that matches the [ at position start,
// correctly handling nested brackets and JSON string literals.
func findMatchingBracket(text string, start int) int {
	return findMatchingDelimiter(text, start, '[', ']')
}

// findMatchingBrace finds the closing } that matches the { at position start,
// correctly handling nested braces and JSON string literals.
func findMatchingBrace(text string, start int) int {
	return findMatchingDelimiter(text, start, '{', '}')
}

// findMatchingDelimiter finds the closing delimiter that matches the opener at position start.
func findMatchingDelimiter(text string, start int, open, close byte) int {
	depth := 0
	inString := false
	escaped := false

	for i := start; i < len(text); i++ {
		ch := text[i]

		if escaped {
			escaped = false
			continue
		}

		if ch == '\\' && inString {
			escaped = true
			continue
		}

		if ch == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
