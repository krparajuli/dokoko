// Package imageconfig loads per-image environment-variable schemas from a
// YAML configuration file.  The schema describes which variables each catalog
// image expects, whether they are required, and what their default values are.
package imageconfig

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// VarDef describes one environment variable expected by an image.
type VarDef struct {
	// Name is the environment variable name (e.g. "ANTHROPIC_API_KEY").
	Name string `yaml:"name"`

	// Required means the variable must be set before the container is
	// provisioned.  If HasDefault is true the default satisfies this
	// requirement automatically.
	Required bool `yaml:"required"`

	// HasDefault indicates DefaultValue is meaningful.
	HasDefault bool `yaml:"hasDefault"`

	// DefaultValue is applied when HasDefault is true and the user has not
	// supplied their own value.
	DefaultValue string `yaml:"defaultValue"`
}

// ImageVarDef associates a set of VarDefs with a catalog image ID.
type ImageVarDef struct {
	// ImageName matches the ID field in the webcontainers catalog (e.g. "claudewebd").
	ImageName string   `yaml:"imagename"`
	Vars      []VarDef `yaml:"vars"`
}

// Config is the top-level structure of the dokoko YAML configuration file.
type Config struct {
	ImageVars []ImageVarDef `yaml:"imagevars"`
}

// Load reads and parses the YAML config file at path.
// If the file does not exist an empty Config is returned without error —
// the application runs fine without a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// FindVars returns the variable schema for the given catalog ID.
// Returns nil if no schema is defined for that image.
func (c *Config) FindVars(catalogID string) []VarDef {
	if c == nil {
		return nil
	}
	for _, iv := range c.ImageVars {
		if iv.ImageName == catalogID {
			return iv.Vars
		}
	}
	return nil
}

// ApplyDefaults returns a copy of stored with any missing default values
// from the schema filled in.  The original map is not mutated.
func (c *Config) ApplyDefaults(catalogID string, stored map[string]string) map[string]string {
	vars := c.FindVars(catalogID)
	if len(vars) == 0 {
		return stored
	}
	merged := make(map[string]string, len(stored)+len(vars))
	for k, v := range stored {
		merged[k] = v
	}
	for _, v := range vars {
		if v.HasDefault {
			if _, already := merged[v.Name]; !already {
				merged[v.Name] = v.DefaultValue
			}
		}
	}
	return merged
}

// MissingRequired returns the names of required variables that are not
// present in stored (after defaults have been considered).
func (c *Config) MissingRequired(catalogID string, stored map[string]string) []string {
	vars := c.FindVars(catalogID)
	var missing []string
	for _, v := range vars {
		if !v.Required {
			continue
		}
		val, ok := stored[v.Name]
		if !ok || val == "" {
			if v.HasDefault && v.DefaultValue != "" {
				continue // default satisfies the requirement
			}
			missing = append(missing, v.Name)
		}
	}
	return missing
}
