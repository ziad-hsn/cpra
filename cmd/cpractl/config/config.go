package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config represents the cpractl configuration file structure.
type Config struct {
	CurrentContext string             `yaml:"current-context"`
	Contexts       map[string]Context `yaml:"contexts"`
}

// Context represents a named server context.
type Context struct {
	Server   string `yaml:"server"`
	Insecure bool   `yaml:"insecure,omitempty"`
}

// Path returns the default config file path.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cpractl", "config.yaml")
}

// Load reads the config file from disk.
func Load() (*Config, error) {
	return LoadFrom(Path())
}

// LoadFrom reads the config file from the provided path.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Save writes the config to disk.
func Save(cfg *Config) error {
	return SaveTo(Path(), cfg)
}

// SaveTo writes the config to the provided path.
func SaveTo(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func defaultConfig() *Config {
	return &Config{
		CurrentContext: "default",
		Contexts:       map[string]Context{"default": {Server: "http://localhost:8080"}},
	}
}
