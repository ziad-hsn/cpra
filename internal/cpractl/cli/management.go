package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func managementKind(name string) (string, error) {
	switch strings.ToLower(name) {
	case "monitor", "monitors", "monitor-configurations", "monitor-configuration", "configurations":
		return "Monitor", nil
	case "notificationendpoint", "notificationendpoints", "notification-endpoint", "notification-endpoints", "endpoint", "endpoints":
		return "NotificationEndpoint", nil
	case "recipient", "recipients", "contact", "contacts":
		return "Recipient", nil
	case "notificationgroup", "notificationgroups", "notification-group", "notification-groups", "group", "groups":
		return "NotificationGroup", nil
	case "credential", "credentials", "secret", "secrets":
		return "Credential", nil
	}
	return "", errors.New("type must be monitor, endpoint, recipient, group or credential (secret)")
}

func managementAddress(args []string, requireID bool) (kind, id string, err error) {
	name, id, err := managementAddressParts(args, requireID)
	if err != nil {
		return "", "", err
	}
	kind, err = managementKind(name)
	return kind, id, err
}

func managementAddressParts(args []string, requireID bool) (name, id string, err error) {
	if len(args) < 1 || len(args) > 2 {
		return "", "", errors.New("specify TYPE/ID or TYPE ID")
	}
	name = args[0]
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		if len(parts) != 2 || len(args) != 1 {
			return "", "", errors.New("specify one TYPE/ID address")
		}
		name, id = parts[0], parts[1]
	} else if len(args) == 2 {
		id = args[1]
	}
	if requireID && id == "" {
		return "", "", errors.New("a stable resource ID is required")
	}
	if id != "" && (id == "." || id == ".." || len(id) > 256 || strings.ContainsAny(id, "/\\?#%\r\n\x00")) {
		return "", "", errors.New("invalid stable resource ID")
	}
	return name, id, nil
}

func addManagementGetCommands(get *cobra.Command, o *options) {
	addManagementIncidentReads(get, o)
	addManagementActionReads(get, o)
	addManagementEventReads(get, o)
	addManagementOperationReads(get, o)
	for _, definition := range []struct {
		name    string
		aliases []string
	}{
		{"monitors", []string{"monitor", "mons", "mon", "monitor-configurations", "monitor-configuration", "configurations"}},
		{"endpoints", []string{"endpoint", "notification-endpoints"}},
		{"recipients", []string{"recipient", "contacts", "contact"}},
		{"groups", []string{"group", "notification-groups"}},
		{"credentials", []string{"credential", "secrets", "secret"}},
	} {
		kindName := definition.name
		list := cpra.ListOptions{Limit: 100}
		command := &cobra.Command{Use: kindName + " [stable-id]", Aliases: definition.aliases, Short: "Read a v2 configuration resource or one bounded page", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			return runManagementGet(cmd, o, append([]string{kindName}, args...), false, list)
		}}
		command.Flags().IntVar(&list.Limit, "limit", 100, "page size (1..500); never collects the fleet automatically")
		command.Flags().StringVar(&list.Cursor, "cursor", "", "continue the server's original resource page")
		command.Flags().StringVarP(&list.Selector, "selector", "l", "", "label selector, when supported by the server")
		get.AddCommand(command)
	}
}

func runManagementGet(cmd *cobra.Command, o *options, args []string, describe bool, list cpra.ListOptions) error {
	if len(args) > 0 && managementIncidentType(strings.SplitN(args[0], "/", 2)[0]) {
		return runManagementIncidentGet(cmd, o, args, describe, list)
	}
	if len(args) > 0 && managementActionType(strings.SplitN(args[0], "/", 2)[0]) {
		return runManagementActionGet(cmd, o, args, describe, list)
	}
	kind, id, err := managementAddress(args, describe)
	if err != nil {
		return err
	}
	if list.Limit < 1 || list.Limit > 500 {
		return errors.New("page limit must be between 1 and 500")
	}
	if id != "" && (list.Cursor != "" || list.Selector != "") {
		return errors.New("cursor and selector apply only to collection reads")
	}
	client, err := o.mustClient()
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	service, err := managementServiceFor(client, kind)
	if err != nil {
		return err
	}
	var reply managementReply
	if id == "" {
		reply, err = service.list(cmd.Context(), list)
	} else {
		reply, err = service.get(cmd.Context(), id)
	}
	if err != nil {
		return err
	}
	return writeManagementReply(cmd, o, reply, describe, false)
}

func addManagementCommands(root *cobra.Command, o *options) {
	addManagementControlCommands(root, o)
	addManagementActionCommands(root, o)
	describe := &cobra.Command{Use: "describe TYPE/ID", Short: "Describe a stable v2 configuration resource and reported status", Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
		return runManagementGet(cmd, o, args, true, cpra.ListOptions{Limit: 100})
	}}
	root.AddCommand(describe)
	for _, verb := range []string{"create", "replace", "patch", "delete"} {
		var file, patchFile, version string
		patchType := "merge"
		command := &cobra.Command{Use: verb + " TYPE/ID", Short: verb + " one v2 resource with explicit concurrency preconditions", Args: cobra.RangeArgs(1, 2)}
		if verb == "create" {
			command.Use = "create TYPE -f FILE|-"
			command.Short = "Create one resource only if its stable ID is absent"
		}
		if verb == "create" || verb == "replace" {
			command.Flags().StringVarP(&file, "filename", "f", "", "one YAML/JSON resource file; - reads stdin")
		}
		if verb == "patch" {
			command.Flags().StringVar(&patchFile, "patch-file", "", "JSON merge patch file; - reads stdin; values are never accepted in argv")
			command.Flags().StringVar(&patchType, "type", "merge", "patch type (merge)")
		}
		if verb != "create" {
			command.Flags().StringVar(&version, "resource-version", "", "the exact resourceVersion observed before this edit")
		}
		command.RunE = func(cmd *cobra.Command, args []string) error {
			kind, id, err := managementAddress(args, verb != "create")
			if err != nil {
				return err
			}
			if verb != "create" && !validManagementVersion(version) {
				return errors.New("--resource-version must contain one explicit, unquoted strong resourceVersion; it is never fetched automatically")
			}
			var resource api.Resource
			var patch api.MergePatch
			if verb == "create" || verb == "replace" {
				resource, err = readManagementResource(cmd, file)
				if err != nil {
					return err
				}
				defer clear(resource.Spec)
				if resource.Kind != kind {
					return errors.New("input kind does not match the selected resource type")
				}
				if id == "" {
					id = resource.Metadata.ID
				} else if id != resource.Metadata.ID {
					return errors.New("input stable ID does not match the selected resource")
				}
				if verb == "create" && (resource.Metadata.UID != "" || resource.Metadata.ResourceVersion != "" || resource.Metadata.Generation != 0) {
					return errors.New("create input must omit server-assigned UID, resourceVersion and generation")
				}
				if verb == "replace" && resource.Metadata.ResourceVersion != "" && resource.Metadata.ResourceVersion != version {
					return errors.New("input resourceVersion differs from --resource-version")
				}
			}
			if verb == "patch" {
				if patchType != "merge" {
					return errors.New("only --type=merge is supported")
				}
				patch, err = readManagementPatch(cmd, patchFile)
				if err != nil {
					return err
				}
				defer clear(patch)
			}
			client, err := o.mustClient()
			if err != nil {
				return err
			}
			defer client.CloseIdleConnections()
			service, err := managementServiceFor(client, kind)
			if err != nil {
				return err
			}
			var reply managementReply
			switch verb {
			case "create":
				reply, err = service.create(cmd.Context(), resource)
			case "replace":
				reply, err = service.replace(cmd.Context(), id, version, resource)
			case "patch":
				reply, err = service.patch(cmd.Context(), id, version, patch)
			case "delete":
				reply, err = service.remove(cmd.Context(), id, version)
			}
			if err != nil {
				return err
			}
			return writeManagementReply(cmd, o, reply, false, true)
		}
		arguments := map[string]string{
			"create":  "monitor -f monitor.yaml",
			"replace": "monitor/api -f monitor.yaml --resource-version OBSERVED_VERSION",
			"patch":   "monitor/api --patch-file patch.json --resource-version OBSERVED_VERSION",
			"delete":  "monitor/api --resource-version OBSERVED_VERSION",
		}
		command.Example = fmt.Sprintf("  cpractl %s %s --server https://localhost:8060 --token-file operator.token", verb, arguments[verb])
		root.AddCommand(command)
	}
}

func validManagementVersion(version string) bool {
	if version == "" || len(version) > 256 || strings.HasPrefix(version, "W/") || strings.ContainsAny(version, "\"\\,*") {
		return false
	}
	for _, b := range []byte(version) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
