package config

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from a dotenv file into the process
// environment. It exists so the runtime can be started without a shell wrapper
// (`set -a; source .env; set +a`) while keeping exported variables as the
// highest-precedence source.
//
// Rules:
//   - A missing file is not an error: dotenv is optional.
//   - A variable already present with a non-empty value in the environment is
//     never overwritten.
//   - Blank lines and lines starting with '#' are ignored.
//   - An optional leading "export " is tolerated.
//   - Single or double surrounding quotes are stripped from the value.
//   - Everything else is literal; there is no interpolation and no multi-line
//     value support in this phase.
func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		value = trimQuotes(value)
		if os.Getenv(key) != "" {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

// trimQuotes strips one pair of matching surrounding single or double quotes.
func trimQuotes(s string) string {
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
