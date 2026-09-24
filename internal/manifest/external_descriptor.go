//go:build externaljobs

package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// ExternalJobIdentity selects an immutable JobType contract, not its current metadata.
type ExternalJobIdentity struct {
	JobTypeID  string
	JobTypeUID string
	Version    string
	Revision   string
	Category   string
}

// ExternalRuntimeKey binds one configured slot to its committed source incarnation.
// It contains no worker parameters or credential-profile alias.
type ExternalRuntimeKey struct {
	SourceKind     string
	SourceID       string
	SourceUID      string
	SourceRevision string
	Slot           string
	JobType        ExternalJobIdentity
}

// ExternalJobDescriptor owns detached inert input. It grants no execution authority.
// Parameters and credential-profile aliases are excluded from formatting and serialization.
type ExternalJobDescriptor struct {
	identity          ExternalJobIdentity
	parameters        []byte
	credentialProfile string
}

// NewExternalJobDescriptor copies bounded input. Registered schema validation and
// encrypted-record authentication remain the management owner's responsibility.
func NewExternalJobDescriptor(identity ExternalJobIdentity, parameters []byte, credentialProfile string) (ExternalJobDescriptor, error) {
	if !validExternalIdentity(identity) || len(parameters) == 0 || len(parameters) > 128<<10 || !utf8.Valid(parameters) || !json.Valid(parameters) || credentialProfile != "" && !externalSelector(credentialProfile) {
		return ExternalJobDescriptor{}, errors.New("invalid external job descriptor")
	}
	return ExternalJobDescriptor{identity: identity, parameters: slices.Clone(parameters), credentialProfile: strings.Clone(credentialProfile)}, nil
}

func (d ExternalJobDescriptor) Identity() ExternalJobIdentity { return d.identity }

// Parameters returns a private copy; callers own its lifetime and clearing.
func (d ExternalJobDescriptor) Parameters() []byte        { return slices.Clone(d.parameters) }
func (d ExternalJobDescriptor) CredentialProfile() string { return d.credentialProfile }
func (d ExternalJobDescriptor) Clone() ExternalJobDescriptor {
	d.parameters = slices.Clone(d.parameters)
	return d
}
func (ExternalJobDescriptor) String() string {
	return "external job descriptor (configuration omitted)"
}
func (d ExternalJobDescriptor) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(d.String())) }
func (ExternalJobDescriptor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("external job descriptor cannot be serialized")
}
func (ExternalJobDescriptor) MarshalYAML() (any, error) {
	return nil, errors.New("external job descriptor cannot be serialized")
}

// ExternalRuntimeBinding associates private input with a nonsecret runtime slot.
// Copies for another owner must use Clone.
type ExternalRuntimeBinding struct {
	Key        ExternalRuntimeKey
	Descriptor ExternalJobDescriptor
}

func (b ExternalRuntimeBinding) Clone() ExternalRuntimeBinding {
	b.Descriptor = b.Descriptor.Clone()
	return b
}
func (ExternalRuntimeBinding) String() string {
	return "external runtime binding (configuration omitted)"
}
func (b ExternalRuntimeBinding) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(b.String())) }
func (ExternalRuntimeBinding) MarshalJSON() ([]byte, error) {
	return nil, errors.New("external runtime binding cannot be serialized")
}
func (ExternalRuntimeBinding) MarshalYAML() (any, error) {
	return nil, errors.New("external runtime binding cannot be serialized")
}

// Validate checks only identity and slot shape; it neither reads storage nor grants work.
func (k ExternalRuntimeKey) Validate() error {
	valid := externalIdentifier(k.SourceID) && externalIdentifier(k.SourceUID) && externalIdentifier(k.SourceRevision) && validExternalIdentity(k.JobType)
	switch k.SourceKind {
	case "Monitor":
		switch k.Slot {
		case "check":
			valid = valid && k.JobType.Category == "check"
		case "recovery":
			valid = valid && k.JobType.Category == "recovery"
		case "notifications.red", "notifications.yellow", "notifications.green", "notifications.cyan", "notifications.gray":
			valid = valid && k.JobType.Category == "notification"
		default:
			valid = false
		}
	case "NotificationEndpoint":
		valid = valid && k.Slot == "endpoint" && k.JobType.Category == "notification"
	default:
		valid = false
	}
	if !valid {
		return errors.New("invalid external runtime key")
	}
	return nil
}
func validExternalIdentity(i ExternalJobIdentity) bool {
	return externalSelector(i.JobTypeID) && externalIdentifier(i.JobTypeUID) && externalSelector(i.Version) && externalIdentifier(i.Revision) && (i.Category == "check" || i.Category == "recovery" || i.Category == "notification")
}
func externalIdentifier(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func externalSelector(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if i != 0 && strings.ContainsRune("._:-", r) {
			continue
		}
		return false
	}
	return true
}
