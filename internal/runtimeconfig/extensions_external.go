//go:build externaljobs

package runtimeconfig

import (
	"reflect"

	"gopkg.in/yaml.v3"
)

type processExtensions struct {
	ExternalJobs ExternalJobs `yaml:"external_jobs,omitempty" json:"-"`
}

// ExternalJobs is a separate process opt-in available only in externaljobs
// builds. Enabling it does not create worker credentials or grant permissions.
type ExternalJobs struct {
	Enabled bool `yaml:"enabled" json:"-"`
}

func (c *ExternalJobs) UnmarshalYAML(node *yaml.Node) error {
	type plain ExternalJobs
	if err := checkManagementNode(node, reflect.TypeFor[plain](), "external_jobs"); err != nil {
		return err
	}
	var value plain
	if err := node.Decode(&value); err != nil {
		return managementConfigError("external_jobs", node.Line, "invalid field type")
	}
	*c = ExternalJobs(value)
	return nil
}

func (c Config) validateExtensions() error {
	if !c.ExternalJobs.Enabled {
		return nil
	}
	if !c.Management.Enabled {
		return managementConfigError("external_jobs.enabled", 0, "requires management.enabled")
	}
	if c.Storage.Mode != "raft" {
		return managementConfigError("external_jobs.enabled", 0, "requires storage.mode raft")
	}
	// Management.Validate has already required a protected transport and an
	// explicit durable wrapping source. Startup verifies those sources before
	// opening management admission; this structural check performs no I/O.
	return nil
}
