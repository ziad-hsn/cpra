package management

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const collectionTicketBytes = 128 << 10

// CollectionCommit rechecks the authenticated principal, permissions and process
// admission at the actual durable write. Crypto runs before this callback.
// Implementations invoke commit synchronously at most once and return its error.
type CollectionCommit func(commit func() error) error

// The outer identity is authenticated through the envelope binding. Claims are
// encrypted: neither the actor nor the private source commitment is a public
// ticket field. A ticket is valid only in its original store and operation epoch.
type collectionTicket struct {
	Version int                   `json:"version"`
	Epoch   string                `json:"epoch"`
	ID      string                `json:"id"`
	Payload secureconfig.Envelope `json:"payload"`
}

type collectionTicketClaims struct {
	Actor         string    `json:"actor"`
	RequestDigest string    `json:"requestDigest"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

func (t collectionTicket) binding(storeID string) secureconfig.Binding {
	return secureconfig.Binding{StoreID: storeID, Kind: "CollectionAdmission", ID: t.ID,
		UID: t.Epoch, Revision: "1", Purpose: "collection-create-ticket"}
}

func collectionIdentityBytes(value *string) ([32]byte, error) {
	var decoded [32]byte
	if value == nil || len(*value) != 64 {
		return decoded, ErrValidation
	}
	if _, err := hex.Decode(decoded[:], []byte(*value)); err != nil || hex.EncodeToString(decoded[:]) != *value {
		clear(decoded[:])
		return decoded, ErrValidation
	}
	return decoded, nil
}

func collectionRequestIdentity(req api.CollectionPrepareRequest, storeID, actor string) (key, source [32]byte, digest string, err error) {
	if string(req.IdentityFormat) != commitment.Format || req.ItemCount < 1 || uint64(req.ItemCount) > commitment.MaxItems ||
		req.NormalizationProfile != "" && req.NormalizationProfile != collection.FileNormalizationProfile ||
		actor == "" || len(actor) > 128 || !utf8.ValidString(actor) || strings.ContainsAny(actor, "\x00\r\n") {
		return key, source, "", ErrValidation
	}
	key, err = collectionIdentityBytes(req.IdentityKey)
	if err == nil {
		source, err = collectionIdentityBytes(req.SourceFingerprint)
	}
	if err == nil {
		_, err = collectionIdentityBytes(&req.ContentDigest)
	}
	if err != nil {
		clear(key[:])
		return key, source, "", err
	}
	h := hmac.New(sha256.New, key[:])
	var size [8]byte
	fields := []string{"cpra/collection/admission/v1", storeID, actor, string(req.IdentityFormat), req.ContentDigest, *req.SourceFingerprint, strconv.FormatInt(req.ItemCount, 10)}
	if req.NormalizationProfile != "" {
		// Preserve the exact legacy ticket identity when no profile was declared.
		// A profiled request cannot consume an unprofiled ticket or vice versa.
		fields[0] = "cpra/collection/admission/v2"
		fields = append(fields, req.NormalizationProfile)
	}
	for _, field := range fields {
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(field))
	}
	return key, source, hex.EncodeToString(h.Sum(nil)), nil
}

// PrepareCollection issues a private, bounded-lifetime creation ticket. The
// caller supplies an admission callback that rechecks current authorization.
// Only the persistent epoch may be initialized; no operation is allocated.
func (c *Catalog) PrepareCollection(ctx context.Context, req api.CollectionPrepareRequest, actor string, now func() time.Time, admit CollectionCommit) (api.CollectionAdmission, error) {
	if ctx == nil || now == nil || admit == nil {
		return api.CollectionAdmission{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.CollectionAdmission{}, err
	}
	if !c.Ready() {
		return api.CollectionAdmission{}, ErrUnavailable
	}
	key, source, digest, err := collectionRequestIdentity(req, c.storeID, actor)
	clear(key[:])
	clear(source[:])
	if err != nil {
		return api.CollectionAdmission{}, err
	}
	at := now().UTC()
	var result []persistence.Result
	err = admit(func() error {
		var submitErr error
		result, submitErr = c.store.Submit(ctx, []persistence.Command{{Kind: "collection", At: now().UTC(),
			Collection: &persistence.CollectionCommand{Action: "epoch", Epoch: uuid.NewString()}}})
		return submitErr
	})
	if err != nil {
		return api.CollectionAdmission{}, err
	}
	if len(result) != 1 || result[0].CollectionEpoch == "" {
		if len(result) == 1 && result[0].Err != nil {
			return api.CollectionAdmission{}, result[0].Err
		}
		return api.CollectionAdmission{}, ErrOutcomeUnconfirmed
	}
	ticket := collectionTicket{Version: 1, Epoch: result[0].CollectionEpoch, ID: uuid.NewString()}
	expires := at.UTC().Add(persistence.CollectionTicketLifetime)
	plain, err := json.Marshal(collectionTicketClaims{Actor: actor, RequestDigest: digest, ExpiresAt: expires})
	if err != nil {
		return api.CollectionAdmission{}, ErrValidation
	}
	defer clear(plain)
	ticket.Payload, err = c.sealer.Seal(ctx, ticket.binding(c.storeID), plain)
	if err != nil {
		return api.CollectionAdmission{}, err
	}
	raw, err := json.Marshal(ticket)
	if err != nil || base64.RawURLEncoding.EncodedLen(len(raw)) > collectionTicketBytes {
		return api.CollectionAdmission{}, ErrUnavailable
	}
	return api.CollectionAdmission{Ticket: base64.RawURLEncoding.EncodeToString(raw), ExpiresAt: expires}, nil
}

func (c *Catalog) openCollectionTicket(ctx context.Context, encoded, actor, digest string) (collectionTicket, collectionTicketClaims, error) {
	var ticket collectionTicket
	var claims collectionTicketClaims
	if len(encoded) == 0 || len(encoded) > collectionTicketBytes {
		return ticket, claims, ErrValidation
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || api.StrictDecode(raw, &ticket) != nil || ticket.Version != 1 ||
		!canonicalUUID(ticket.ID) || !canonicalUUID(ticket.Epoch) || ticket.Payload.Validate() != nil || len(ticket.Payload.Ciphertext) > 1024 {
		return collectionTicket{}, claims, ErrValidation
	}
	plain, err := c.sealer.Open(ctx, ticket.binding(c.storeID), ticket.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return collectionTicket{}, claims, ctx.Err()
		}
		// Caller-supplied invalid ciphertext cannot poison catalog readiness.
		if errors.Is(err, secureconfig.ErrMissingKey) || errors.Is(err, secureconfig.ErrUnwrap) {
			return collectionTicket{}, claims, ErrUnavailable
		}
		return collectionTicket{}, claims, ErrValidation
	}
	defer clear(plain)
	if api.StrictDecode(plain, &claims) != nil || claims.Actor != actor || claims.ExpiresAt.IsZero() ||
		!hmac.Equal([]byte(claims.RequestDigest), []byte(digest)) {
		return collectionTicket{}, collectionTicketClaims{}, ErrValidation
	}
	return ticket, claims, nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

// CreateCollection consumes the original ticket or reconciles its original
// handle. The state machine checks epoch, expiry and identity atomically before
// allocation. A lost reply must be retried with this exact request and ticket.
// The caller supplies a synchronous authorization callback for the actual Submit.
func (c *Catalog) CreateCollection(ctx context.Context, req api.OperationCreateRequest, actor string, now func() time.Time, admit CollectionCommit) (api.Operation, error) {
	if ctx == nil || now == nil || admit == nil {
		return api.Operation{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return api.Operation{}, err
	}
	if !c.Ready() {
		return api.Operation{}, ErrUnavailable
	}
	identity := api.CollectionPrepareRequest{ContentDigest: req.ContentDigest, IdentityFormat: api.CollectionPrepareRequestIdentityFormat(req.IdentityFormat),
		IdentityKey: req.IdentityKey, SourceFingerprint: req.SourceFingerprint, ItemCount: req.ItemCount, NormalizationProfile: req.NormalizationProfile}
	key, source, digest, err := collectionRequestIdentity(identity, c.storeID, actor)
	defer clear(key[:])
	defer clear(source[:])
	if err != nil {
		return api.Operation{}, err
	}
	ticket, claims, err := c.openCollectionTicket(ctx, req.AdmissionTicket, actor, digest)
	if err != nil {
		return api.Operation{}, err
	}
	at := now().UTC()
	head := persistence.CollectionState{UploadID: ticket.ID, Actor: actor, IdentityFormat: commitment.Format, ContentDigest: req.ContentDigest, NormalizationProfile: req.NormalizationProfile,
		ItemCount: uint64(req.ItemCount), MaxEncodedBytes: 1 << 30, Phase: "uploading", ProgressDigest: persistence.CollectionInitialDigest(),
		CreatedAt: at.UTC(), ActivityAt: at.UTC(), ExpiresAt: at.UTC().Add(persistence.CollectionInactivityLifetime),
		Admission: &persistence.CollectionAdmissionProof{Epoch: ticket.Epoch, RequestDigest: digest, ExpiresAt: claims.ExpiresAt}}
	head.Secret, err = sealCollectionIdentity(ctx, c.sealer, head, c.storeID, key[:], source[:])
	if err != nil {
		return api.Operation{}, err
	}
	var result []persistence.Result
	err = admit(func() error {
		at = now().UTC()
		owner, ownerErr := c.store.ObserveCollectionOwner(ctx, actor, at)
		if ownerErr != nil {
			return ownerErr
		}
		head.Owner = owner
		head.CreatedAt, head.ActivityAt, head.ExpiresAt = at, at, at.Add(persistence.CollectionInactivityLifetime)
		var submitErr error
		result, submitErr = c.store.Submit(ctx, []persistence.Command{{Kind: "collection", At: at, Collection: &persistence.CollectionCommand{
			Action: "create", Epoch: ticket.Epoch, Create: &head}}})
		if submitErr != nil {
			return errors.Join(ErrOutcomeUnconfirmed, submitErr)
		}
		return nil
	})
	if err != nil {
		return api.Operation{}, err
	}
	if len(result) != 1 {
		return api.Operation{}, ErrOutcomeUnconfirmed
	}
	if result[0].Err != nil {
		return api.Operation{}, result[0].Err
	}
	id := result[0].CollectionID
	if id == "" {
		return api.Operation{}, ErrOutcomeUnconfirmed
	}
	receipt, err := c.store.CollectionReceipt(ctx, id, at)
	if err != nil {
		return api.Operation{ID: id}, err
	}
	return collectionOperationView(receipt), nil
}

func collectionOperationView(r persistence.CollectionReceipt) api.Operation {
	result := api.Operation{ID: r.ID, IdentityFormat: api.OperationIdentityFormat(r.IdentityFormat), ContentDigest: r.ContentDigest, NormalizationProfile: r.NormalizationProfile,
		ItemCount: api.Pointer(int64(r.ItemCount)), Uploaded: api.Pointer(int64(r.Uploaded)), State: r.Phase,
		Committed: api.Pointer(int64(0)), Applied: api.Pointer(int64(0))}
	if r.Activation != nil {
		result.ExecutionResult = &api.ExecutionResultAvailability{State: "pending"}
		// An admission receipt alone does not carry execution counters. Do not
		// report invented zeroes if a caller lacks the protected observation.
		result.Committed, result.Applied = nil, nil
	}
	if r.Execution != nil {
		counts := executionSummaryCounts(*r.Execution)
		result.ExecutionResult = &api.ExecutionResultAvailability{State: "pending", Counts: &counts}
		result.Committed, result.Applied = api.Pointer(counts.Accepted), api.Pointer(counts.ChildApplied)
	}
	if r.ExecutionObservation != nil {
		availability := collectionExecutionObservationResult(*r.ExecutionObservation)
		result.ExecutionResult = &availability
		result.Committed = api.Pointer(availability.Counts.Accepted)
		result.Applied = api.Pointer(availability.Counts.ChildApplied)
	}
	// Pending and interrupted work has no verdict. False is reserved for the
	// original sealed rejection; it must never stand in for missing evidence.
	if (r.Phase == "validated" || r.Phase == "rejected") && r.Validation != nil {
		result.Validated = api.Pointer(r.Validation.Header.Valid)
	}
	if r.Phase == "validating" || r.Phase == "applying" || result.ExecutionResult != nil && result.ExecutionResult.State == "pending" {
		result.RetryAfterSeconds = 5
	}
	return result
}
