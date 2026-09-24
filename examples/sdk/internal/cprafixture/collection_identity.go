package cprafixture

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// This fixture retains private input only in process memory. It demonstrates
// the SDK wire contract; it has no encrypted Raft store or restart guarantee.
type prepared struct {
	identity    api.CollectionPrepareRequest
	expires     time.Time
	operationID string
}

func (h *Handler) prepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		problem(w, 405, "POST required")
		return
	}
	var request api.CollectionPrepareRequest
	if !decode(w, r, &request, 4096) {
		return
	}
	if !validIdentity(request) {
		problem(w, 400, "invalid fixture collection identity")
		return
	}
	if len(h.admissions) >= 10000 {
		problem(w, 429, "fixture admission capacity reached")
		return
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		problem(w, 503, "fixture admission unavailable")
		return
	}
	ticket := hex.EncodeToString(nonce[:])
	expires := time.Now().Add(time.Hour)
	h.admissions[ticket] = &prepared{identity: request, expires: expires}
	write(w, 200, api.CollectionAdmission{Ticket: ticket, ExpiresAt: expires})
}

func creationIdentity(r api.OperationCreateRequest) api.CollectionPrepareRequest {
	return api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount}
}

func validIdentity(r api.CollectionPrepareRequest) bool {
	if r.IdentityFormat != commitment.Format || r.IdentityKey == nil || r.SourceFingerprint == nil || r.ItemCount < 1 || r.ItemCount > 10000 {
		return false
	}
	for _, value := range []string{*r.IdentityKey, *r.SourceFingerprint, r.ContentDigest} {
		raw, err := hex.DecodeString(value)
		valid := err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
		clear(raw)
		if !valid {
			return false
		}
	}
	return true
}

func sameIdentity(a, b api.CollectionPrepareRequest) bool {
	aRaw, _ := json.Marshal(a)
	bRaw, _ := json.Marshal(b)
	defer clear(aRaw)
	defer clear(bRaw)
	return bytes.Equal(aRaw, bRaw)
}

func itemPosition(item api.ApplyItem) commitment.Position {
	return commitment.Position{ID: item.ID, Ordinal: uint64(item.Ordinal), Source: commitment.SourcePosition{Token: item.Source, Document: uint64(item.SourceDocument), Item: uint64(item.SourceItem)}}
}

func validInventory(o *staged) bool {
	key, _ := hex.DecodeString(*o.identity.IdentityKey)
	defer clear(key)
	fingerprint, _ := hex.DecodeString(*o.identity.SourceFingerprint)
	a, err := commitment.NewAccumulator(key, uint64(o.count), [32]byte(fingerprint))
	if err != nil {
		return false
	}
	defer a.Close()
	items := make([]api.ApplyItem, 0, len(o.items))
	for _, item := range o.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Ordinal < items[j].Ordinal })
	for _, item := range items {
		mac, err := hex.DecodeString(item.ContentDigest)
		if err != nil || len(mac) != 32 || a.Add(itemPosition(item), [32]byte(mac)) != nil {
			return false
		}
	}
	digest, _ := hex.DecodeString(o.identity.ContentDigest)
	return a.Verify(digest) == nil
}
