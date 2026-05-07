package championsfetch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveInputPath resuelve la lista tournaments.json (--input > CHAMPIONS_INPUT_JSON > cwd).
func ResolveInputPath(cwd, cliInput string) (string, error) {
	if p := strings.TrimSpace(cliInput); p != "" {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, abs)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("--input %s: %w", abs, err)
		}
		if fi.IsDir() {
			return "", fmt.Errorf("--input es un directorio: %s", abs)
		}
		return filepath.Clean(abs), nil
	}
	if env := strings.TrimSpace(os.Getenv("CHAMPIONS_INPUT_JSON")); env != "" {
		abs := env
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(cwd, env)
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("CHAMPIONS_INPUT_JSON %s: %w", abs, err)
		}
		if fi.IsDir() {
			return "", fmt.Errorf("CHAMPIONS_INPUT_JSON es un directorio: %s", abs)
		}
		return filepath.Clean(abs), nil
	}
	tails := []string{"tournaments.json", filepath.Join("docs", "tournaments.json")}
	for _, t := range tails {
		cand := filepath.Join(cwd, t)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return filepath.Clean(cand), nil
		}
	}
	return "", fmt.Errorf("no encuentro tournaments.json en %s (usa --input o docs/tournaments.json)", cwd)
}
