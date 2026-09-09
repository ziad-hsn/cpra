package schema

import "time"

// New intervention (remediation action) types added on top of the general SDK.
// Config structs always compile (no SDK dependency); only the job
// implementations are build-tagged.

// InterventionTargetKubernetes restarts or scales a Kubernetes workload.
type InterventionTargetKubernetes struct {
	Type           string        `yaml:"type" json:"type"`
	KubeconfigPath string        `yaml:"kubeconfig_path" json:"kubeconfig_path"`
	Namespace      string        `yaml:"namespace" json:"namespace"`
	Kind           string        `yaml:"kind" json:"kind"` // deployment | statefulset | replicaset
	Name           string        `yaml:"name" json:"name"`
	Replicas       *int          `yaml:"replicas" json:"replicas"` // nil = rollout restart; non-nil = scale
	Timeout        time.Duration `yaml:"timeout" json:"timeout"`
}

func (i *InterventionTargetKubernetes) Copy() InterventionTarget {
	c := &InterventionTargetKubernetes{
		Type:           i.Type,
		KubeconfigPath: i.KubeconfigPath,
		Namespace:      i.Namespace,
		Kind:           i.Kind,
		Name:           i.Name,
		Timeout:        i.Timeout,
	}
	if i.Replicas != nil {
		v := *i.Replicas
		c.Replicas = &v
	}
	return c
}

func (i *InterventionTargetKubernetes) GetTargetType() string { return i.Type }

// InterventionTargetWebhook triggers an external system via an HTTP request.
type InterventionTargetWebhook struct {
	Type    string            `yaml:"type" json:"type"`
	URL     string            `yaml:"url" json:"url"`
	Method  string            `yaml:"method" json:"method"` // default POST
	Headers map[string]string `yaml:"headers" json:"headers"`
	Body    string            `yaml:"body" json:"body"`
	Timeout time.Duration     `yaml:"timeout" json:"timeout"`
}

func (i *InterventionTargetWebhook) Copy() InterventionTarget {
	headers := make(map[string]string, len(i.Headers))
	for k, v := range i.Headers {
		headers[k] = v
	}
	return &InterventionTargetWebhook{
		Type:    i.Type,
		URL:     i.URL,
		Method:  i.Method,
		Headers: headers,
		Body:    i.Body,
		Timeout: i.Timeout,
	}
}

func (i *InterventionTargetWebhook) GetTargetType() string { return i.Type }

// InterventionTargetSystemd restarts a systemd unit on the local host.
type InterventionTargetSystemd struct {
	Type    string        `yaml:"type" json:"type"`
	Unit    string        `yaml:"unit" json:"unit"`
	Mode    string        `yaml:"mode" json:"mode"` // replace (default) | fail | isolate | reload-or-restart
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
}

func (i *InterventionTargetSystemd) Copy() InterventionTarget {
	return &InterventionTargetSystemd{
		Type:    i.Type,
		Unit:    i.Unit,
		Mode:    i.Mode,
		Timeout: i.Timeout,
	}
}

func (i *InterventionTargetSystemd) GetTargetType() string { return i.Type }

// InterventionTargetAWS performs a cloud-provider remediation action (EC2
// reboot as the first concrete operation).
type InterventionTargetAWS struct {
	Type       string        `yaml:"type" json:"type"`
	Region     string        `yaml:"region" json:"region"`
	Operation  string        `yaml:"operation" json:"operation"` // reboot-instance
	InstanceID string        `yaml:"instance_id" json:"instance_id"`
	Timeout    time.Duration `yaml:"timeout" json:"timeout"`
}

func (i *InterventionTargetAWS) Copy() InterventionTarget {
	return &InterventionTargetAWS{
		Type:       i.Type,
		Region:     i.Region,
		Operation:  i.Operation,
		InstanceID: i.InstanceID,
		Timeout:    i.Timeout,
	}
}

func (i *InterventionTargetAWS) GetTargetType() string { return i.Type }
