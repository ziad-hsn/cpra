package collection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// FileNormalizationProfile identifies the versioned base file contract shared
// by browser imports and server-assisted reselection. It is independent of the
// inventory HMAC format, compiler version and source-build hash. External job
// resources and drivers are excluded even when this package has externaljobs.
const FileNormalizationProfile = "cpra.file.base.v1"

// ErrUnsupportedNormalization identifies an unavailable normalization profile.
// An absent profile is not silently interpreted as the current profile.
var ErrUnsupportedNormalization = errors.New("unsupported collection normalization profile")

var errFileUTF8 = errors.New("collection source is not valid UTF-8")

// NormalizedItem contains the exact resource JSON and coordinates used by the
// file profile. JSON belongs to the callback; callers must not serialize the
// resource again before computing its inventory commitment. It may hold secrets.
type NormalizedItem struct {
	ID       string
	Location Location
	JSON     []byte
}

func (NormalizedItem) String() string               { return "private normalized collection item (input omitted)" }
func (i NormalizedItem) GoString() string           { return i.String() }
func (i NormalizedItem) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(i.String())) }
func (NormalizedItem) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private normalized collection items cannot be serialized")
}

// NormalizeFile streams one UTF-8 YAML/JSON source through the named profile.
// It does not open, close, seek or stage the reader. The callback owns each
// result, but must stage without activation: a later malformed item can fail
// after earlier callbacks. A nil error proves parsing finished, not reference
// validation, authorization or identity equality with an existing operation.
//
// The base profile uses Decode's manifest normalization, document/item coordinates
// and duplicate detection followed by encoding/json resource serialization.
// Its ceilings are 64 MiB of raw source, 16 MiB for the decoder's document
// metadata accounting, 1 MiB per resource and 10,000 resources. The document
// counter measures YAML metadata lines or JSON non-list field-value bytes;
// it does not measure a whole raw document or all decoded allocations. Lists
// stream entries independently. Zero limits select those ceilings; nonzero
// limits may tighten but never expand them.
// SourceName is local diagnostic metadata and is not part of normalized JSON.
// Ordinary Decode and Freeze retain their separate, configurable contracts.
func NormalizeFile(ctx context.Context, r io.Reader, profile string, options DecodeOptions, visit func(NormalizedItem) error) error {
	if profile != FileNormalizationProfile {
		return ErrUnsupportedNormalization
	}
	if r == nil || visit == nil {
		return errors.New("source reader and staging callback are required")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = 64 << 20
	}
	if options.MaxDocumentBytes == 0 {
		options.MaxDocumentBytes = 16 << 20
	}
	if options.MaxResourceBytes == 0 {
		options.MaxResourceBytes = 1 << 20
	}
	if options.MaxResources == 0 {
		options.MaxResources = 10_000
	}
	if options.MaxBytes > 64<<20 || options.MaxDocumentBytes > 16<<20 || options.MaxResourceBytes > 1<<20 || options.MaxResources > 10_000 {
		return errors.New("file normalization profile limits exceeded")
	}
	return Decode(ctx, &fileUTF8Reader{r: io.LimitReader(r, options.MaxBytes+1)}, options, func(item Item) error {
		if err := ValidateFileProfileResource(profile, item.Resource); err != nil {
			return fmt.Errorf("%s: %w", item.Location, err)
		}
		raw, err := json.Marshal(item.Resource)
		if err != nil {
			return errors.New("normalized resource encoding failed")
		}
		return visit(NormalizedItem{ID: item.ID, Location: item.Location, JSON: raw})
	})
}

// ValidateFileProfileResource checks whether a decoded resource belongs to the
// named file normalization profile. It neither serializes nor changes the
// resource and does not validate its schema, references or provider settings.
// Callers must separately validate the resource before admission. Empty and
// unknown profiles return ErrUnsupportedNormalization; callers preserving an
// unprofiled input contract must explicitly omit this check for that contract.
func ValidateFileProfileResource(profile string, resource api.Resource) error {
	if profile != FileNormalizationProfile {
		return ErrUnsupportedNormalization
	}
	return fileBaseResource(resource)
}

// Keep this projection explicit: linked tagged API validators also accept
// external drivers, including notifyType selectors without an inline driver.
func fileBaseResource(r api.Resource) error {
	unsupported := errors.New("resource is outside the base file normalization profile")
	switch r.Kind {
	case "Credential", "Recipient", "NotificationGroup":
		return nil
	case "NotificationEndpoint":
		var driver api.DriverConfig
		if json.Unmarshal(r.Spec, &driver) != nil || !fileBaseDriver("notification", driver.Type) {
			return unsupported
		}
	case "Monitor":
		var spec api.MonitorSpec
		if json.Unmarshal(r.Spec, &spec) != nil || !fileBaseDriver("check", spec.Check.Driver.Type) {
			return unsupported
		}
		if spec.Recovery != nil && !fileBaseDriver("recovery", spec.Recovery.Driver.Type) {
			return unsupported
		}
		if spec.Notifications != nil {
			for _, rule := range *spec.Notifications {
				if rule.Driver != nil && !fileBaseDriver("notification", rule.Driver.Type) {
					return unsupported
				}
				if rule.NotifyType != nil && !fileBaseDriver("notification", *rule.NotifyType) {
					return unsupported
				}
			}
		}
	default:
		return unsupported
	}
	return nil
}

func fileBaseDriver(category, kind string) bool {
	switch category + "/" + kind {
	case "check/http", "check/tcp", "check/icmp", "check/dns", "check/udp", "check/grpc", "check/docker",
		"check/redis", "check/postgres", "check/mysql", "check/mongo", "check/rabbitmq", "check/kafka", "check/tls",
		"recovery/kubernetes", "recovery/webhook", "recovery/systemd", "recovery/aws", "recovery/docker",
		"notification/email", "notification/webhook", "notification/log", "notification/pagerduty", "notification/slack",
		"notification/telegram", "notification/discord", "notification/opsgenie", "notification/teams", "notification/mattermost",
		"notification/pushover", "notification/twilio", "notification/datadog", "notification/victorops":
		return true
	}
	return false
}

// fileUTF8Reader rejects malformed bytes before the parser can replace them.
// A split UTF-8 sequence retains at most three bytes between bounded reads.
type fileUTF8Reader struct {
	r                  io.Reader
	buffer             [32 << 10]byte
	next, valid, total int
	terminal           error
}

func (r *fileUTF8Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.next < r.valid {
		n := copy(p, r.buffer[r.next:r.valid])
		r.next += n
		return n, nil
	}
	if r.terminal != nil {
		return 0, r.terminal
	}
	tail := copy(r.buffer[:], r.buffer[r.valid:r.total])
	n, err := r.r.Read(r.buffer[tail:])
	r.next, r.valid, r.total = 0, 0, tail+n
	for r.valid < r.total {
		rest := r.buffer[r.valid:r.total]
		if !utf8.FullRune(rest) {
			if err != nil {
				r.terminal = errFileUTF8
			}
			break
		}
		rune, size := utf8.DecodeRune(rest)
		if rune == utf8.RuneError && size == 1 {
			r.terminal = errFileUTF8
			break
		}
		r.valid += size
	}
	if r.terminal == nil && err != nil {
		r.terminal = err
	}
	if r.valid > 0 {
		n = copy(p, r.buffer[:r.valid])
		r.next = n
		return n, nil
	}
	return 0, r.terminal
}
