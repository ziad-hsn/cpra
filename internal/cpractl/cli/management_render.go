package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func safeManagementHandle(value string) bool {
	epoch := value
	if len(value) != 36 {
		if len(value) != 60 || !strings.HasPrefix(value, "op.") || value[39] != '.' {
			return false
		}
		epoch = value[3:39]
		for _, digit := range value[40:] {
			if digit < '0' || digit > '9' {
				return false
			}
		}
		sequence, err := strconv.ParseUint(value[40:], 10, 64)
		if err != nil || sequence == 0 {
			return false
		}
	}
	parsed, err := uuid.Parse(epoch)
	return err == nil && parsed != uuid.Nil && parsed.String() == epoch
}
func managementDisplay(value string) string {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

func writeManagementReply(cmd *cobra.Command, o *options, reply managementReply, describe, mutation bool) error {
	defer clear(reply.data)
	var err error
	switch o.output {
	case formatJSON:
		var formatted bytes.Buffer
		if json.Indent(&formatted, reply.data, "", "  ") != nil {
			return errors.New("invalid management response JSON")
		}
		formatted.WriteByte('\n')
		_, err = cmd.OutOrStdout().Write(formatted.Bytes())
	case formatYAML:
		var node yaml.Node
		if yaml.Unmarshal(reply.data, &node) != nil {
			return errors.New("invalid management response YAML conversion")
		}
		encoder := yaml.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent(2)
		err = encoder.Encode(&node)
		closeErr := encoder.Close()
		if err == nil {
			err = closeErr
		}
	default:
		if reply.incidents {
			err = writeManagementIncidents(cmd, o, reply.data, describe)
		} else if reply.actions {
			err = writeManagementActions(cmd, o, reply.data, describe)
		} else if reply.operations {
			err = writeManagementOperations(cmd, reply.data)
		} else if reply.events {
			err = writeManagementEvents(cmd, o, reply.data)
		} else {
			err = writeManagementTable(cmd, o, reply.data, describe)
		}
	}
	if err != nil {
		return err
	}
	if mutation && safeManagementHandle(reply.operationID) {
		_, err = fmt.Fprintf(cmd.ErrOrStderr(), "Operation: %s (inspect its progress before assuming controller application)\n", reply.operationID)
	}
	return err
}

func writeManagementTable(cmd *cobra.Command, o *options, data []byte, describe bool) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return errors.New("invalid management response")
	}
	if _, operation := fields["contentDigest"]; operation {
		var value api.Operation
		if json.Unmarshal(data, &value) != nil {
			return errors.New("invalid operation response")
		}
		return writeManagementOperation(cmd, value)
	}
	var resources []api.Resource
	if items, ok := fields["items"]; ok {
		if json.Unmarshal(items, &resources) != nil {
			return errors.New("invalid management page")
		}
	} else {
		var resource api.Resource
		if json.Unmarshal(data, &resource) != nil {
			return errors.New("invalid management resource")
		}
		resources = []api.Resource{resource}
	}
	if describe && len(resources) == 1 {
		resource := resources[0]
		name := ""
		if resource.Metadata.Name != nil {
			name = *resource.Metadata.Name
		}
		status := string(resource.Status)
		if status == "" || status == "{}" || status == "null" {
			status = "unavailable (the server did not report resource status)"
		}
		return kv(cmd.OutOrStdout(), [][2]string{{"API version", managementDisplay(resource.APIVersion)}, {"Kind", managementDisplay(resource.Kind)}, {"Stable ID", managementDisplay(resource.Metadata.ID)}, {"Name", managementDisplay(name)}, {"UID", managementDisplay(resource.Metadata.UID)}, {"Resource version", managementDisplay(resource.Metadata.ResourceVersion)}, {"Generation", strconv.FormatInt(resource.Metadata.Generation, 10)}, {"Configuration", string(resource.Spec)}, {"Status", status}})
	}
	headers := []string{"KIND", "ID", "NAME", "RESOURCE VERSION"}
	if o.output == formatWide {
		headers = append(headers, "UID", "GENERATION")
	}
	rows := make([][]string, 0, len(resources))
	for _, resource := range resources {
		name := ""
		if resource.Metadata.Name != nil {
			name = *resource.Metadata.Name
		}
		row := []string{managementDisplay(resource.Kind), managementDisplay(resource.Metadata.ID), managementDisplay(name), managementDisplay(resource.Metadata.ResourceVersion)}
		if o.output == formatWide {
			row = append(row, managementDisplay(resource.Metadata.UID), strconv.FormatInt(resource.Metadata.Generation, 10))
		}
		rows = append(rows, row)
	}
	if err := table(cmd.OutOrStdout(), headers, rows); err != nil {
		return err
	}
	var cursor string
	_ = json.Unmarshal(fields["nextCursor"], &cursor)
	if cursor != "" {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next cursor: %s\n", managementDisplay(cursor))
		return err
	}
	return nil
}

func operationCount(value *int64) string {
	if value == nil || *value < 0 {
		return "unavailable"
	}
	return strconv.FormatInt(*value, 10)
}
