package cpra

import (
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

var errCollectionIdentity = errors.New("invalid collection identity or inventory")

func collectionHex(value string) ([commitment.MACBytes]byte, error) {
	var result [commitment.MACBytes]byte
	if len(value) != 2*commitment.MACBytes {
		return result, errCollectionIdentity
	}
	for _, b := range value {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return result, errCollectionIdentity
		}
	}
	_, err := hex.Decode(result[:], []byte(value))
	if err != nil {
		return [commitment.MACBytes]byte{}, errCollectionIdentity
	}
	return result, nil
}

func collectionIdentity(format string, key, fingerprint *string, digest string, count, minimum int64) ([commitment.KeyBytes]byte, [commitment.MACBytes]byte, [commitment.MACBytes]byte, error) {
	var secret [commitment.KeyBytes]byte
	var source, content [commitment.MACBytes]byte
	if format != commitment.Format || key == nil || fingerprint == nil || count < minimum || uint64(count) > commitment.MaxItems {
		return secret, source, content, errCollectionIdentity
	}
	var err error
	secret, err = collectionHex(*key)
	if err == nil {
		source, err = collectionHex(*fingerprint)
	}
	if err == nil {
		content, err = collectionHex(digest)
	}
	if err != nil {
		clear(secret[:])
		return [commitment.KeyBytes]byte{}, source, content, errCollectionIdentity
	}
	return secret, source, content, nil
}

func validateCollectionCreation(req api.OperationCreateRequest) error {
	if len(req.AdmissionTicket) == 0 || len(req.AdmissionTicket) > 128<<10 || !supportedCollectionProfile(req.NormalizationProfile) {
		return errCollectionIdentity
	}
	key, _, _, err := collectionIdentity(string(req.IdentityFormat), req.IdentityKey, req.SourceFingerprint, req.ContentDigest, req.ItemCount, 1)
	clear(key[:])
	return err
}

func validateCollectionPreparation(req api.CollectionPrepareRequest) error {
	if !supportedCollectionProfile(req.NormalizationProfile) {
		return errCollectionIdentity
	}
	key, _, _, err := collectionIdentity(string(req.IdentityFormat), req.IdentityKey, req.SourceFingerprint, req.ContentDigest, req.ItemCount, 1)
	clear(key[:])
	return err
}

func supportedCollectionProfile(profile string) bool {
	// Keep the low-level client independent of the collection helper package,
	// which imports this client. The wire contract owns the accepted profile.
	return profile == "" || profile == "cpra.file.base.v1"
}

func collectionItem(item api.ApplyItem) (commitment.Position, []byte, [commitment.MACBytes]byte, error) {
	position := commitment.Position{Ordinal: uint64(item.Ordinal), ID: item.ID, Source: commitment.SourcePosition{Token: item.Source, Document: uint64(item.SourceDocument), Item: uint64(item.SourceItem)}}
	if item.ID != item.Resource.Kind+"/"+item.Resource.Metadata.ID {
		return position, nil, [commitment.MACBytes]byte{}, errCollectionIdentity
	}
	raw, err := json.Marshal(item.Resource)
	if err != nil {
		return position, nil, [commitment.MACBytes]byte{}, errCollectionIdentity
	}
	// A zero test key validates bounds/framing only. Upload requests do not carry
	// an inventory key; the server must verify against its encrypted operation.
	if _, err = commitment.ItemMAC(make([]byte, commitment.KeyBytes), position, raw); err != nil {
		return position, nil, [commitment.MACBytes]byte{}, errCollectionIdentity
	}
	mac, err := collectionHex(item.ContentDigest)
	return position, raw, mac, err
}

func validateCollectionUpload(req api.UploadRequest) error {
	if len(req.Items) == 0 || len(req.Items) > 256 {
		return errCollectionIdentity
	}
	seen := make(map[string]struct{}, len(req.Items))
	var previous uint64
	for index, item := range req.Items {
		position, _, _, err := collectionItem(item)
		if err != nil {
			return err
		}
		if _, duplicate := seen[item.ID]; duplicate || index > 0 && position.Ordinal != previous+1 {
			return errCollectionIdentity
		}
		seen[item.ID] = struct{}{}
		previous = position.Ordinal
	}
	return nil
}

func validateCollectionPreflight(req api.PreflightRequest) error {
	if len(req.Items) > 10_000 {
		return errCollectionIdentity
	}
	key, source, content, err := collectionIdentity(string(req.IdentityFormat), req.IdentityKey, req.SourceFingerprint, req.ContentDigest, req.ItemCount, 0)
	if err != nil {
		return err
	}
	defer clear(key[:])
	if int64(len(req.Items)) != req.ItemCount {
		return errCollectionIdentity
	}
	a, err := commitment.NewAccumulator(key[:], uint64(req.ItemCount), source)
	if err != nil {
		return errCollectionIdentity
	}
	defer a.Close()
	seen := make(map[string]struct{}, len(req.Items))
	for _, item := range req.Items {
		position, raw, mac, err := collectionItem(item)
		if err != nil {
			return err
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return errCollectionIdentity
		}
		seen[item.ID] = struct{}{}
		if commitment.VerifyItem(key[:], position, raw, mac[:]) != nil || a.Add(position, mac) != nil {
			return errCollectionIdentity
		}
	}
	if a.Verify(content[:]) != nil {
		return errCollectionIdentity
	}
	return nil
}
