package management

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const (
	collectionValidationProfileVersion = 1
	// Bump this reviewed policy version whenever inert Go checks, credential
	// resolution, routing, prefix ordering or allocation/work accounting change
	// accepted configurations or validation decisions. Schema and limit changes
	// are also hashed, but cannot identify arbitrary Go implementation changes.
	collectionValidationPolicyVersion = 2
	collectionProfileSchemaBytes      = 1 << 20
	collectionProfileEncodedBytes     = 64 << 10
	collectionProfileDriverCount      = 64
	collectionProfileResourceCount    = 16
	collectionProfileHashDomain       = "cpra/collection/validation-profile/v1\x00"
)

// All fields have a fixed JSON order; lists are sorted and contain no duplicates.
// Available means compiled built-in support, never connectivity or runtime
// authorization. Merely embedding an external-job schema enables no driver.
type collectionValidationProfileData struct {
	ProfileVersion          uint64                     `json:"profileVersion"`
	APIVersion              string                     `json:"apiVersion"`
	SchemaDigest            string                     `json:"schemaDigest"`
	ValidationPolicyVersion uint64                     `json:"validationPolicyVersion"`
	CompilerVersion         string                     `json:"compilerVersion"`
	PlanCodecVersion        uint64                     `json:"planCodecVersion"`
	GOOS                    string                     `json:"goos"`
	Resources               []string                   `json:"resources"`
	Drivers                 []collectionProfileDriver  `json:"drivers"`
	Limits                  collectionValidationLimits `json:"limits"`
}

type collectionProfileDriver struct {
	Kind        string `json:"kind"`
	Driver      string `json:"driver"`
	Available   bool   `json:"available"`
	ConfigModel string `json:"configModel"`
}

type collectionProfileRuntimeModel struct{ kind, driver, model string }

type collectionValidationLimits struct {
	ResourceBytes      uint64 `json:"resourceBytes"`
	InputItems         uint64 `json:"inputItems"`
	GraphVersions      uint64 `json:"graphVersions"`
	GraphVisits        uint64 `json:"graphVisits"`
	GraphMetadataBytes uint64 `json:"graphMetadataBytes"`
	BorrowedBytes      uint64 `json:"borrowedBytes"`
	PlanMetadataBytes  uint64 `json:"planMetadataBytes"`
	PlanVisits         uint64 `json:"planVisits"`
	PlanBytes          uint64 `json:"planBytes"`
	PlanFragmentBytes  uint64 `json:"planFragmentBytes"`
	PlanChunkEntries   uint64 `json:"planChunkEntries"`
	ResultBytes        uint64 `json:"resultBytes"`
	ResultItemBytes    uint64 `json:"resultItemBytes"`
}

func collectionValidationProfileDefaults() collectionValidationProfileData {
	return collectionValidationProfileData{ProfileVersion: collectionValidationProfileVersion, APIVersion: api.APIVersion,
		ValidationPolicyVersion: collectionValidationPolicyVersion, CompilerVersion: persistence.CollectionPlanCompilerVersion,
		PlanCodecVersion: persistence.CollectionPlanCodecVersion, GOOS: runtime.GOOS,
		Limits: collectionValidationLimits{ResourceBytes: api.MaxResourceBytes, InputItems: commitment.MaxItems,
			GraphVersions: maxValidationGraph, GraphVisits: collectionValidationVisits, GraphMetadataBytes: collectionValidationBytes,
			BorrowedBytes: collectionSourcePlaintextBudget, PlanMetadataBytes: collectionPlanMetadataBytes,
			PlanVisits: collectionPlanVisits, PlanBytes: persistence.CollectionPlanMaxBytes,
			PlanFragmentBytes: persistence.CollectionPlanMaxFragmentBytes, PlanChunkEntries: persistence.CollectionPlanMaxChunkEntries,
			ResultBytes: persistence.CollectionValidationResultMaxBytes, ResultItemBytes: persistence.CollectionValidationItemMaxBytes}}
}

type collectionValidationProfileResult struct {
	profile collectionValidationProfileData
	digest  string
}

var cachedCollectionValidationProfile = sync.OnceValues(func() (collectionValidationProfileResult, error) {
	models, err := collectionValidationRuntimeModels(runtimeDriverFactories)
	if err != nil {
		return collectionValidationProfileResult{}, err
	}
	profile, digest, err := buildCollectionValidationProfile(collectionValidationProfileDefaults(), api.Schema(), ResourceKinds(), jobs.Capabilities(), models)
	return collectionValidationProfileResult{profile: profile, digest: digest}, err
})

// collectionValidationProfile identifies this build's validation semantics. It
// performs no provider/job construction, credential access or catalog lookup.
// The cache owns immutable build inputs; returned lists belong to the caller.
// It is a future coordinator input, not an execution or authorization grant.
func collectionValidationProfile() (collectionValidationProfileData, string, error) {
	result, err := cachedCollectionValidationProfile()
	profile := result.profile
	profile.Resources = slices.Clone(profile.Resources)
	profile.Drivers = slices.Clone(profile.Drivers)
	return profile, result.digest, err
}

// These reviewed factories allocate only zero-valued legacy configuration
// structs. Their concrete names match the selected API schema models, a
// relationship qualified independently for every built-in driver in tests.
func collectionValidationRuntimeModels(factories map[string]func() any) ([]collectionProfileRuntimeModel, error) {
	if len(factories) == 0 || len(factories) > collectionProfileDriverCount {
		return nil, ErrUnavailable
	}
	models := make([]collectionProfileRuntimeModel, 0, len(factories))
	for key, factory := range factories {
		kind, driver, ok := strings.Cut(key, "/")
		if !ok || !collectionProfileDriverIdentity(kind, driver) || factory == nil {
			return nil, ErrUnavailable
		}
		value := reflect.ValueOf(factory())
		if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() || value.Type().Elem().Kind() != reflect.Struct {
			return nil, ErrUnavailable
		}
		target := value.Type().Elem()
		if target.PkgPath() != "github.com/ziad-hsn/cpra/internal/manifest" || !value.Elem().IsZero() || !collectionProfileName(target.Name()) {
			return nil, ErrUnavailable
		}
		models = append(models, collectionProfileRuntimeModel{kind: kind, driver: driver, model: target.Name()})
	}
	return models, nil
}

// Build-derived input only. The selected schema is bounded before decoding;
// hashing its exact bytes intentionally changes identity even for documentation
// or formatting changes. This is not a JSON Schema canonicalizer.
func buildCollectionValidationProfile(base collectionValidationProfileData, schema []byte, resources []string, capabilities []jobs.Capability, models []collectionProfileRuntimeModel) (collectionValidationProfileData, string, error) {
	if len(schema) == 0 || len(schema) > collectionProfileSchemaBytes || len(resources) == 0 || len(resources) > collectionProfileResourceCount ||
		len(capabilities) == 0 || len(capabilities) > collectionProfileDriverCount || len(models) != len(capabilities) ||
		base.SchemaDigest != "" || len(base.Resources) != 0 || len(base.Drivers) != 0 {
		return collectionValidationProfileData{}, "", ErrUnavailable
	}
	var contract struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if json.Unmarshal(schema, &contract) != nil || contract.Components.Schemas == nil {
		return collectionValidationProfileData{}, "", ErrUnavailable
	}
	concreteModel := func(name string) bool {
		var definition struct {
			Type string `json:"type"`
			Ref  string `json:"$ref"`
		}
		raw, exists := contract.Components.Schemas[name]
		return exists && json.Unmarshal(raw, &definition) == nil && definition.Type == "object" && definition.Ref == ""
	}
	base.Resources = slices.Clone(resources)
	slices.Sort(base.Resources)
	for i, kind := range base.Resources {
		if !collectionProfileName(kind) || !concreteModel(kind) || i > 0 && base.Resources[i-1] == kind {
			return collectionValidationProfileData{}, "", ErrUnavailable
		}
	}
	mapping := make(map[string]string, len(models))
	for _, model := range models {
		key := model.kind + "/" + model.driver
		if !collectionProfileDriverIdentity(model.kind, model.driver) || !collectionProfileName(model.model) || !concreteModel(model.model) || mapping[key] != "" {
			return collectionValidationProfileData{}, "", ErrUnavailable
		}
		mapping[key] = model.model
	}
	base.Drivers = make([]collectionProfileDriver, 0, len(capabilities))
	for _, capability := range capabilities {
		key := capability.Kind + "/" + capability.Driver
		model := mapping[key]
		if !collectionProfileDriverIdentity(capability.Kind, capability.Driver) || model == "" {
			return collectionValidationProfileData{}, "", ErrUnavailable
		}
		delete(mapping, key) // Also rejects duplicate capability entries.
		base.Drivers = append(base.Drivers, collectionProfileDriver{Kind: capability.Kind, Driver: capability.Driver, Available: capability.Available, ConfigModel: model})
	}
	if len(mapping) != 0 {
		return collectionValidationProfileData{}, "", ErrUnavailable
	}
	slices.SortFunc(base.Drivers, func(a, b collectionProfileDriver) int {
		if order := strings.Compare(a.Kind, b.Kind); order != 0 {
			return order
		}
		return strings.Compare(a.Driver, b.Driver)
	})
	hash := sha256.Sum256(schema)
	base.SchemaDigest = hex.EncodeToString(hash[:])
	raw, err := encodeCollectionValidationProfile(base)
	if err != nil {
		return collectionValidationProfileData{}, "", err
	}
	h := sha256.New()
	_, _ = h.Write([]byte(collectionProfileHashDomain))
	_, _ = h.Write(raw)
	return base, hex.EncodeToString(h.Sum(nil)), nil
}

func collectionProfileName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func collectionProfileDriverIdentity(kind, driver string) bool {
	return (kind == "check" || kind == "recovery" || kind == "notification") && collectionProfileName(driver)
}

func encodeCollectionValidationProfile(profile collectionValidationProfileData) ([]byte, error) {
	if profile.ProfileVersion != collectionValidationProfileVersion || profile.ValidationPolicyVersion == 0 || profile.PlanCodecVersion == 0 ||
		profile.APIVersion == "" || len(profile.APIVersion) > 128 || !collectionProfileName(profile.CompilerVersion) || !collectionProfileName(profile.GOOS) ||
		len(profile.Resources) == 0 || len(profile.Resources) > collectionProfileResourceCount || len(profile.Drivers) == 0 || len(profile.Drivers) > collectionProfileDriverCount {
		return nil, ErrUnavailable
	}
	var digest [sha256.Size]byte
	if len(profile.SchemaDigest) != hex.EncodedLen(len(digest)) {
		return nil, ErrUnavailable
	}
	if _, err := hex.Decode(digest[:], []byte(profile.SchemaDigest)); err != nil || hex.EncodeToString(digest[:]) != profile.SchemaDigest {
		return nil, ErrUnavailable
	}
	for i, kind := range profile.Resources {
		if !collectionProfileName(kind) || i > 0 && profile.Resources[i-1] >= kind {
			return nil, ErrUnavailable
		}
	}
	for i, driver := range profile.Drivers {
		if !collectionProfileDriverIdentity(driver.Kind, driver.Driver) || !collectionProfileName(driver.ConfigModel) ||
			i > 0 && (profile.Drivers[i-1].Kind > driver.Kind || profile.Drivers[i-1].Kind == driver.Kind && profile.Drivers[i-1].Driver >= driver.Driver) {
			return nil, ErrUnavailable
		}
	}
	l := profile.Limits
	for _, value := range []uint64{l.ResourceBytes, l.InputItems, l.GraphVersions, l.GraphVisits, l.GraphMetadataBytes, l.BorrowedBytes,
		l.PlanMetadataBytes, l.PlanVisits, l.PlanBytes, l.PlanFragmentBytes, l.PlanChunkEntries, l.ResultBytes, l.ResultItemBytes} {
		if value == 0 {
			return nil, ErrUnavailable
		}
	}
	raw, err := json.Marshal(profile)
	if err != nil || len(raw) > collectionProfileEncodedBytes {
		return nil, ErrUnavailable
	}
	return raw, nil
}
