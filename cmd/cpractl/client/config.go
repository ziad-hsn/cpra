package client

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

// ConfigPath returns the default config file path.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cpractl", "config.yaml")
}

// LoadConfig reads the config file from disk.
func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{
				CurrentContext: "default",
				Contexts:       map[string]Context{"default": {Server: "http://localhost:8080"}},
			}, nil
		}
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// SaveConfig writes the config to disk.
func SaveConfig(cfg *Config) error {
	dir := filepath.Dir(ConfigPath())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(), data, 0644)
}
