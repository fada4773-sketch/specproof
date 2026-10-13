package scenario

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Settings is the settings file of a contract test (contract-settings.yaml):
// how the test runs, not what it sends. Paths in it are relative to the
// file. A value is either written as is or read from the environment with
// "env:NAME".
type Settings struct {
	// Spec is the OpenAPI file.
	Spec string `json:"spec"`
	// BaseURL is the instance the requests go to, with base path.
	BaseURL string `json:"baseURL"`
	// Health is requested before the first step and must answer 2xx.
	Health string `json:"health"`
	Tokens struct {
		// Default is sent with every step without "token".
		Default string `json:"default"`
		// Forbidden is a valid token with too few rights, for the steps
		// with "token: forbidden".
		Forbidden string `json:"forbidden"`
	} `json:"tokens"`
	// Headers are added to every request.
	Headers map[string]string `json:"headers"`
	// LearnFrom is a running stage whose records give the generator
	// realistic values (GET requests only).
	LearnFrom *Remote `json:"learnFrom"`
	// Records is how many records the generator creates per resource.
	Records int `json:"records"`
	// Report is the Markdown report; HTML and JSON go next to it.
	Report string `json:"report"`
	// Unique makes the fields the scenario marks as unique new for every
	// run, for an instance that is not empty.
	Unique bool `json:"unique"`

	// Path is the settings file; Dir its directory.
	Path string `json:"-"`
	Dir  string `json:"-"`
}

// Remote is a running instance read with GET requests only.
type Remote struct {
	BaseURL string `json:"baseURL"`
	Token   string `json:"token"`
}

var settingsKeys = []string{"spec", "baseURL", "health", "tokens", "headers", "learnFrom", "records", "report", "unique"}

// LoadSettings reads a settings file.
func LoadSettings(path string) (*Settings, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := decodeYAML(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	m, ok := v.(map[string]any)
	if !ok && v != nil {
		return nil, fmt.Errorf("%s: the settings must be a mapping", path)
	}
	for k := range m {
		if !slices.Contains(settingsKeys, k) {
			return nil, fmt.Errorf("%s: unknown key %q (known: %s)", path, k, strings.Join(settingsKeys, ", "))
		}
	}
	s := &Settings{}
	if err := into(m, s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s.Path, s.Dir = abs, filepath.Dir(abs)
	if s.Records < 0 {
		return nil, fmt.Errorf("%s: records must not be negative", path)
	}
	return s, nil
}

// Rel resolves a path of the settings file against its directory.
func (s *Settings) Rel(p string) string {
	if p == "" || filepath.IsAbs(p) || s == nil || s.Dir == "" {
		return p
	}
	return filepath.Join(s.Dir, p)
}

// Expand returns a value of the settings: "env:NAME" reads the environment
// variable; it is an error if it is not set. Any other value is returned
// as is.
func Expand(v string) (string, error) {
	name, ok := strings.CutPrefix(v, "env:")
	if !ok {
		return v, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New(`"env:" needs the name of an environment variable`)
	}
	val, set := os.LookupEnv(name)
	if !set || val == "" {
		return "", fmt.Errorf("environment variable %s is not set", name)
	}
	return val, nil
}

// Literal reports whether a value is written in the file itself, not read
// from the environment; a token written in a committed file is a risk.
func Literal(v string) bool {
	return v != "" && !strings.HasPrefix(v, "env:")
}
