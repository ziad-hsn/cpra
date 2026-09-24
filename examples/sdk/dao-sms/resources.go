//go:build externaljobs

package main

import (
	"context"
	"encoding/json"
	"fmt"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

const rpcParameterSchema = `{"type":"object","additionalProperties":false,"required":["expectedChainID","governor","maxBlockAgeSeconds"],"properties":{"expectedChainID":{"type":"string","pattern":"^0x(0|[1-9a-fA-F][0-9a-fA-F]*)$"},"governor":{"type":"string","pattern":"^0x[0-9a-fA-F]{40}$"},"maxBlockAgeSeconds":{"type":"integer","minimum":1,"maximum":3600}}}`
const smsParameterSchema = `{"type":"object","additionalProperties":false,"required":["recipientAlias","text"],"properties":{"recipientAlias":{"type":"string","minLength":1,"maxLength":64},"text":{"type":"string","minLength":1,"maxLength":480}}}`
const rpcResultSchema = `{"type":["object","null"],"additionalProperties":false,"required":["chainID","blockNumber","blockAgeSeconds"],"properties":{"chainID":{"type":"string"},"blockNumber":{"type":"string"},"blockAgeSeconds":{"type":"integer","minimum":0}}}`
const smsResultSchema = `{"type":["object","null"],"additionalProperties":false,"required":["gatewayAcceptanceID"],"properties":{"gatewayAcceptanceID":{"type":"string","minLength":1,"maxLength":128}}}`

func resources(parameters rpcParameters) ([]api.Resource, error) {
	if err := parameters.validate(); err != nil {
		return nil, err
	}
	rpcRaw, err := json.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	smsRaw, err := json.Marshal(smsParameters{RecipientAlias: "dao-oncall", Text: "CPRa: governance RPC health failed. Inspect the dao-governance-rpc monitor and incident before taking action."})
	if err != nil {
		return nil, err
	}
	check, err := api.Driver("check", "external", api.ExternalConfig{JobTypeID: rpcJobType, Version: jobVersion, CredentialProfile: rpcProfile, Parameters: rpcRaw})
	if err != nil {
		return nil, err
	}
	notification, err := api.Driver("notification", "external", api.ExternalConfig{JobTypeID: smsJobType, Version: jobVersion, CredentialProfile: smsProfile, Parameters: smsRaw})
	if err != nil {
		return nil, err
	}
	groupID := "dao-oncall"
	dispatch := true
	values := []any{
		api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: rpcJobType}, Spec: api.JobTypeSpec{Handler: rpcJobType, Kind: "check", Version: jobVersion, ProtocolVersion: "1", Timeout: "5s", ParameterSchema: json.RawMessage(rpcParameterSchema), ResultSchema: json.RawMessage(rpcResultSchema)}},
		api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: smsJobType}, Spec: api.JobTypeSpec{Handler: smsJobType, Kind: "notification", Version: jobVersion, ProtocolVersion: "1", Timeout: "5s", RejectionCodes: []string{"invalid_configuration", "gateway_rejected"}, ParameterSchema: json.RawMessage(smsParameterSchema), ResultSchema: json.RawMessage(smsResultSchema)}},
		api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "dao-sms"}, Spec: notification},
		api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: groupID}, Spec: api.NotificationGroupSpec{EndpointRefs: []string{"dao-sms"}}},
		api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: monitorID}, Spec: api.MonitorSpec{
			Check:         api.CheckSpec{Driver: check, Interval: "60s", Timeout: "5s"},
			Notifications: &map[string]api.AlertRule{"red": {Dispatch: &dispatch, GroupRef: &groupID}},
		}},
	}
	output := make([]api.Resource, 0, len(values))
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		resource, err := api.DecodeResource(raw)
		if err != nil {
			return nil, err
		}
		output = append(output, resource)
	}
	return output, nil
}

// Freeze all five descriptors before staging any write. Apply validates the
// whole collection on the server, which owns dependency order and per-item CAS.
func register(ctx context.Context, client *cpra.Client, parameters rpcParameters) (collection.Result, error) {
	items, err := resources(parameters)
	if err != nil {
		return collection.Result{}, err
	}
	frozen, err := collection.FreezeResources(ctx, collection.Slice(items), collection.Options{MaxResources: 5, MaxStagingBytes: 1 << 20})
	if err != nil {
		return collection.Result{}, err
	}
	defer frozen.Close()
	if _, err = collection.ValidateReferences(ctx, frozen, nil); err != nil {
		return collection.Result{}, err
	}
	result, err := collection.Apply(ctx, client.Operations, frozen)
	if err != nil {
		return result, err
	}
	if result.OperationID == "" {
		return result, fmt.Errorf("registration did not return an operation handle")
	}
	result, err = collection.Wait(ctx, client.Operations, result)
	if err == nil && (result.Operation.ExecutionResult == nil || result.Operation.ExecutionResult.Summary == nil || result.Operation.ExecutionResult.Summary.Outcome != "completed") {
		return result, fmt.Errorf("registration ended in %s; inspect its item outcomes before changing inputs", result.Operation.State)
	}
	return result, err
}
