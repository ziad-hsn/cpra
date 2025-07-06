package parser

import (
	"cpra/internal/loader/schema"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
)

type FieldType struct {
	Required bool
}

type parseState struct {
	line                     int
	monitorName              string
	pulseType                string
	interventionTarget       string
	interventionTargetFields string
	codeColor                string
	fields                   string
}

var (
	MonitorFields = map[string]FieldType{
		"name":          {Required: true},
		"enabled":       {Required: false},
		"pulse_check":   {Required: true},
		"intervention":  {Required: false},
		"codes":         {Required: false},
		"notify_groups": {Required: false},
	}
	PulseFields = map[string]FieldType{
		"type":         {Required: true},
		"interval":     {Required: true},
		"timeout":      {Required: true},
		"max_failures": {Required: false},
		"config":       {Required: true},
	}
	PulseConfigHTTPFields = map[string]FieldType{
		"url":     {Required: true},
		"method":  {Required: false},
		"headers": {Required: false},
		"auth":    {Required: false},
		"retries": {Required: false},
	}

	PulseConfigTCPFields = map[string]FieldType{
		"host":    {Required: true},
		"port":    {Required: true},
		"retries": {Required: false},
	}

	PulseConfigICMPFields = map[string]FieldType{
		"host":             {Required: true},
		"count":            {Required: false},
		"retries":          {Required: false},
		"ignore_privilege": {Required: false},
	}

	// TODO

	//PulseConfigGRPCFields   = map[string]FieldType{}
	//PulseConfigDockerFields = map[string]FieldType{}

	// ????
	//PulseConfigTLSFields = map[string]FieldType{}
	//PulseConfigUDPFields = map[string]FieldType{}
	//PulseConfigDNSFields = map[string]FieldType{}

	InterventionFields = map[string]FieldType{
		"action":  {Required: true},
		"retries": {Required: false},
		"target":  {Required: true},
	}
	InterventionTargetDockerFields = map[string]FieldType{
		"type":      {Required: false},
		"container": {Required: true},
	}
	CodeFields = map[string]FieldType{
		"groups": {Required: false},
		"red":    {Required: false},
		"yellow": {Required: false},
		"green":  {Required: false},
		"cyan":   {Required: false},
		"gray":   {Required: false},
	}

	CodeColorFields = map[string]FieldType{
		"groups":   {Required: false},
		"dispatch": {Required: false},
		"notify":   {Required: true},
		"config":   {Required: true},
	}
)

var ManifestFields = map[string]map[string]FieldType{
	"monitors":            MonitorFields,
	"pulse_check":         PulseFields,
	"pulse_check_http":    PulseConfigHTTPFields,
	"pulse_check_tcp":     PulseConfigTCPFields,
	"pulse_check_icmp":    PulseConfigICMPFields,
	"intervention":        InterventionFields,
	"intervention_docker": InterventionTargetDockerFields,
	"codes":               CodeFields,
	"code_color":          CodeColorFields,
}

type YamlParser struct {
}

func NewYamlParser() *YamlParser {
	return &YamlParser{}
}

func (p *YamlParser) Parse(r io.Reader) (schema.Manifest, error) {

	var state parseState
	var manifest schema.Manifest
	decoder := yaml.NewDecoder(r)

	for {
		var node map[string]yaml.Node
		err := decoder.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			return schema.Manifest{}, err
		}
		for key, value := range node {
			if key == "monitors" {
				var monitorsNode []yaml.Node
				if err := value.Decode(&monitorsNode); err != nil {
					return schema.Manifest{}, err
				}
				seen := map[string]struct{}{}

				for _, monitor := range monitorsNode {

					m, err := p.ParseMonitor(monitor, &state)
					if err != nil {
						return schema.Manifest{}, err
					}
					name := state.monitorName
					if _, exists := seen[name]; exists {
						return schema.Manifest{}, &duplicateMonitorNameError{
							name: name,
							line: monitor.Content[0].Line,
						}
					}
					seen[state.monitorName] = struct{}{}

					manifest.Monitors = append(manifest.Monitors, m)
				}
			}
		}

	}

	return manifest, nil
}

func (p *YamlParser) ParseMonitor(m yaml.Node, state *parseState) (schema.Monitor, error) {
	var keys map[string]yaml.Node
	monitor := schema.Monitor{}
	if err := m.Decode(&keys); err != nil {
		return schema.Monitor{}, err
	}
	node, ok := keys["name"]
	if !ok {
		node := m.Content[0]
		return schema.Monitor{}, &requiredMonitorFieldError{
			field:     "name",
			parentKey: "monitor",
			line:      node.Line,
			reason:    ErrRequiredField,
		}
	}
	state.monitorName = node.Value
	state.line = node.Line
	key, err := checkMissingRequiredKey("monitors", keys)

	if err != nil || key != "" {
		return schema.Monitor{}, &requiredMonitorFieldError{
			field:     key,
			parentKey: "monitor",
			line:      keys[key].Line,
			reason:    ErrRequiredField,
		}
	}

	for k := range keys {
		err := isValidKey(k, "monitors")
		if err != nil {
			line := keys[k].Line

			return schema.Monitor{}, &invalidMonitorFieldError{parentKey: "monitor", monitor: state.monitorName, field: k, line: line, reason: err}
		}

	}
	monitor.Name = state.monitorName
	if enabled, ok := keys["enabled"]; ok {
		if enabled.Value == "false" {
			monitor.Enabled = false
		} else {
			monitor.Enabled = true
		}
	} else {
		monitor.Enabled = true
	}

	pulseNode := keys["pulse_check"]

	pulse, err := p.ParsePulse(pulseNode, state)

	if err != nil {
		return schema.Monitor{}, err
	}

	monitor.Pulse = pulse

	interventionNode := keys["intervention"]

	intervention, err := p.ParseIntervention(interventionNode, state)
	if err != nil {
		return schema.Monitor{}, err
	}
	monitor.Intervention = intervention

	codeNode := keys["codes"]
	codes, err := p.ParseCode(codeNode, state)
	if err != nil {
		return schema.Monitor{}, err
	}
	monitor.Codes = codes

	return monitor, nil
}

func (p *YamlParser) ParsePulse(pNode yaml.Node, state *parseState) (schema.Pulse, error) {
	var keys map[string]yaml.Node

	if err := pNode.Decode(&keys); err != nil {
		return schema.Pulse{}, err
	}

	key, err := checkMissingRequiredKey("pulse_check", keys)

	if err != nil || key != "" {
		return schema.Pulse{}, &requiredMonitorFieldError{
			field:     key,
			parentKey: "pulse_check",
			line:      keys[key].Line,
			reason:    ErrRequiredField,
		}
	}

	for k := range keys {
		err := isValidKey(k, "pulse_check")
		if err != nil {
			line := keys[k].Line

			return schema.Pulse{}, &invalidMonitorFieldError{parentKey: "pulse_check", monitor: state.monitorName, field: k, line: line, reason: err}
		}
	}

	state.pulseType = keys["type"].Value

	switch state.pulseType {
	case "http":
		state.fields = "pulse_check_http"
	case "tcp":
		state.fields = "pulse_check_tcp"
	case "icmp":
		state.fields = "pulse_check_icmp"

	default:
		return schema.Pulse{}, &invalidMonitorFieldError{parentKey: "pulse_check", monitor: state.monitorName, field: "type", line: keys["type"].Line, reason: ErrUnknownField}
	}

	state.line = pNode.Content[0].Line
	pConfig := keys["config"]
	config, err := p.ParsePulseConfig(pConfig, state)
	if err != nil {
		return schema.Pulse{}, err
	}

	var pulse schema.Pulse
	err = pNode.Decode(&pulse)
	if err != nil {
		return schema.Pulse{}, err
	}
	pulse.Config = config
	return pulse, nil
}

func (p *YamlParser) ParsePulseConfig(pNode yaml.Node, state *parseState) (schema.PulseConfig, error) {
	var keys map[string]yaml.Node
	if err := pNode.Decode(&keys); err != nil {
		return nil, err
	}
	state.line = pNode.Content[0].Line

	key, err := checkMissingRequiredKey(state.fields, keys)

	if err != nil || key != "" {
		return nil, &requiredMonitorFieldError{
			field:     key,
			parentKey: fmt.Sprintf("%v pulse_check", state.pulseType),
			line:      state.line,
			reason:    ErrRequiredField,
		}
	}

	for k := range keys {
		err := isValidKey(k, state.fields)
		if err != nil {
			line := keys[k].Line
			return nil, &invalidMonitorFieldError{parentKey: "pulse_check", monitor: state.monitorName, field: k, line: line, reason: fmt.Errorf("invalid pulse config for type %q %w", keys["type"].Value, err)}
		}
	}

	pulseType, err := decodePulseConfig(pNode, state.pulseType)

	if err != nil {
		return nil, err
	}

	return pulseType, nil
}

func (p *YamlParser) ParseIntervention(i yaml.Node, state *parseState) (schema.Intervention, error) {
	var keys map[string]yaml.Node
	if err := i.Decode(&keys); err != nil {
		return schema.Intervention{}, err
	}

	key, err := checkMissingRequiredKey("intervention", keys)

	if err != nil || key != "" {
		return schema.Intervention{}, &requiredMonitorFieldError{
			field:     key,
			parentKey: "intervention",
			line:      keys[key].Line,
			reason:    ErrRequiredField,
		}
	}

	for k := range keys {
		err := isValidKey(k, "intervention")
		if err != nil {
			line := keys[k].Line

			return schema.Intervention{}, &invalidMonitorFieldError{parentKey: "intervention", monitor: state.monitorName, field: k, line: line, reason: err}
		}
	}
	iTarget := keys["target"]

	var target map[string]yaml.Node
	if err := iTarget.Decode(&target); err != nil {
		return schema.Intervention{}, err
	}

	//targetType, ok := target["type"]
	//if !ok {
	//	return schema.Intervention{}, &requiredMonitorFieldError{
	//		field:     "type",
	//		parentKey: "intervention.target",
	//		line:      targetType.Line,
	//		reason:    ErrRequiredField,
	//	}
	//}
	action := keys["action"]
	switch action.Value {
	case "docker":
		state.interventionTargetFields = "intervention_docker"
		err := p.ParseInterventionTarget(iTarget, state)
		if err != nil {
			return schema.Intervention{}, err
		}
	default:
		return schema.Intervention{}, &invalidMonitorFieldError{parentKey: "intervention", monitor: state.monitorName, field: "target", line: keys["target"].Content[0].Line, reason: ErrUnknownField}
	}
	var intervention schema.Intervention
	err = i.Decode(&intervention)
	if err != nil {
		return schema.Intervention{}, err
	}
	//pulse.Config = config
	return intervention, nil
}

func (p *YamlParser) ParseInterventionTarget(i yaml.Node, state *parseState) error {
	var keys map[string]yaml.Node

	if err := i.Decode(&keys); err != nil {
		return err
	}

	key, err := checkMissingRequiredKey(state.interventionTargetFields, keys)

	if err != nil || key != "" {
		return &requiredMonitorFieldError{
			field:     key,
			parentKey: fmt.Sprintf("%v intervention", keys["type"].Value),
			line:      keys[key].Line,
			reason:    ErrRequiredField,
		}
	}

	for k := range keys {
		err := isValidKey(k, state.interventionTargetFields)
		if err != nil {
			line := keys[k].Line
			return &invalidMonitorFieldError{parentKey: fmt.Sprintf("%v intervention", keys["type"].Value), monitor: state.monitorName, field: k, line: line, reason: err}

		}
	}
	return nil
}

func (p *YamlParser) ParseCode(c yaml.Node, state *parseState) (schema.Codes, error) {
	var keys map[string]yaml.Node
	if err := c.Decode(&keys); err != nil {
		return nil, err
	}
	var codes schema.Codes
	if err := c.Decode(&codes); err != nil {
		return nil, err
	}
	key, err := checkMissingRequiredKey("codes", keys)
	if err != nil || key != "" {
		return nil, &requiredMonitorFieldError{
			field:     key,
			parentKey: "codes",
			line:      keys[key].Line,
			reason:    ErrRequiredField,
		}
	}

	//codes := make(schema.Codes)
	for k, _ := range keys {
		err := isValidKey(k, "codes")
		if err != nil {
			line := keys[k].Line
			return nil, &invalidMonitorFieldError{parentKey: "codes", monitor: state.monitorName, field: k, line: line, reason: err}
		}

	}
	return codes, nil
}

func (p *YamlParser) ParseCodeColor(c yaml.Node, state *parseState) error {
	var keys map[string]yaml.Node
	if err := c.Decode(&keys); err != nil {
		return err
	}

	key, err := checkMissingRequiredKey("codes", keys)

	if err != nil || key != "" {
		return &requiredMonitorFieldError{
			field:     key,
			parentKey: state.codeColor,
			line:      keys[key].Line,
			reason:    ErrRequiredField,
		}
	}

	for k := range keys {
		err := isValidKey(k, "code_color")
		if err != nil {
			line := keys[k].Line
			return &invalidMonitorFieldError{parentKey: state.codeColor, monitor: state.monitorName, field: k, line: line, reason: err}
		}
	}
	return nil
}
