package collection_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// This is a transport interoperability fixture, not an implemented CPRa server
// collection activation protocol. Source coordinates use the canonical request
// fields; this fixture implements only raw-span verification, with no providers.
// Deliberately unusual spec values exercise serialization, not driver validation.
func TestInventoryExactResourceBytesSurvivePublicSDKUpload(t *testing.T) {
	for _, spec := range []string{
		`{ "text": "<>&", "escaped\u004bey": "quotes\"slash\\", "htmlKey<>&": true }`,
		"{\"text\":\"literal\u2028and\u2029\",\"escaped\":\"\\u2028\\u2029\"}",
		`{"integer":18446744073709551615,"aboveJSPrecision":9007199254740993,"exp":1e+09,"fraction":1.00,"negativeZero":-0}`,
	} {
		t.Run("case", func(t *testing.T) {
			resource := api.Resource{APIVersion: "cpra.io/v2", Kind: "Monitor", Metadata: api.Metadata{ID: "serialization-fixture", Name: api.Pointer("serialization-fixture")}, Spec: json.RawMessage(spec)}
			frozen, err := json.Marshal(resource)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(frozen, []byte(`"spec":{`)) {
				t.Fatal("resource object was not preserved")
			}
			key := bytes.Repeat([]byte{0x41}, commitment.KeyBytes)
			token, _ := commitment.SourceToken(1)
			position := commitment.Position{Ordinal: 1, ID: "Monitor/serialization-fixture", Source: commitment.SourcePosition{Token: token, Document: 1, Item: 1}}
			mac, err := commitment.ItemMAC(key, position, frozen)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPut || r.URL.Path != "/api/v2/operations/original/items" || r.Header.Get("Authorization") != "Bearer local-serializer-token" {
					t.Error("unexpected SDK request")
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
				if err != nil || len(body) > 4<<20 {
					t.Error("unbounded/failed request body")
					w.WriteHeader(400)
					return
				}
				var request struct {
					Items []struct {
						ID, ContentDigest, Source           string
						Ordinal, SourceDocument, SourceItem int64
						Resource                            json.RawMessage
					}
				}
				if err = json.Unmarshal(body, &request); err != nil || len(request.Items) != 1 {
					t.Error("invalid object upload")
					w.WriteHeader(400)
					return
				}
				item := request.Items[0]
				if item.ID != position.ID || item.Source != token || item.Ordinal != 1 || item.SourceDocument != 1 || item.SourceItem != 1 || !bytes.Equal(item.Resource, frozen) {
					t.Error("public SDK changed the frozen resource span")
					w.WriteHeader(400)
					return
				}
				expected, err := hex.DecodeString(item.ContentDigest)
				wirePosition := commitment.Position{Ordinal: uint64(item.Ordinal), ID: item.ID, Source: commitment.SourcePosition{Token: item.Source, Document: uint64(item.SourceDocument), Item: uint64(item.SourceItem)}}
				if err != nil || commitment.VerifyItem(key, wirePosition, item.Resource, expected) != nil {
					t.Error("SDK upload no longer verifies")
					w.WriteHeader(400)
					return
				}
				// A decoded generic map would round large numbers and change escape
				// spellings. Verification uses the raw span before semantic decoding.
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(api.Operation{ID: "original", State: "staging", Uploaded: api.Pointer(int64(1))})
			}))
			defer server.Close()
			client, err := cpra.New(cpra.Config{BaseURL: server.URL, HTTPClient: server.Client(), AuthToken: "local-serializer-token"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			reply, err := client.Operations.Upload(context.Background(), "original", api.UploadRequest{Items: []api.ApplyItem{{ID: position.ID, Source: token, Ordinal: 1, SourceDocument: 1, SourceItem: 1, ContentDigest: hex.EncodeToString(mac[:]), Resource: resource}}})
			if err != nil || reply == nil || reply.Data.Uploaded == nil || *reply.Data.Uploaded != 1 || calls.Load() != 1 {
				t.Fatal("public SDK upload did not retain exact committed bytes", err)
			}
		})
	}
}

func TestInventorySerializerDriftIsRejectedWithoutRetry(t *testing.T) {
	resource := api.Resource{APIVersion: "cpra.io/v2", Kind: "Monitor", Metadata: api.Metadata{ID: "serialization-fixture", Name: api.Pointer("serialization-fixture")}, Spec: json.RawMessage(`{"text":"<>&"}`)}
	actual, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	// Semantically identical but different bytes are not an equivalent frozen
	// input. This resembles a caller calculating its MAC with different escaping.
	drift := []byte(strings.ReplaceAll(string(actual), `\u003c`, "<"))
	if bytes.Equal(drift, actual) {
		t.Fatal("fixture did not alter escaping")
	}
	key := bytes.Repeat([]byte{0x41}, commitment.KeyBytes)
	token, _ := commitment.SourceToken(1)
	position := commitment.Position{Ordinal: 1, ID: "Monitor/serialization-fixture", Source: commitment.SourcePosition{Token: token, Document: 1, Item: 1}}
	mac, err := commitment.ItemMAC(key, position, drift)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Items []struct{ Resource json.RawMessage }
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&request); err != nil || len(request.Items) != 1 {
			t.Error("invalid upload fixture")
			w.WriteHeader(400)
			return
		}
		if !errors.Is(commitment.VerifyItem(key, position, request.Items[0].Resource, mac[:]), commitment.ErrMismatch) {
			t.Error("serializer drift accepted")
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"about:blank","title":"Invalid collection identity","status":400,"code":"invalidInventory"}`)
	}))
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, HTTPClient: server.Client(), AuthToken: "local-serializer-token", ReadAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, err = client.Operations.Upload(context.Background(), "original", api.UploadRequest{Items: []api.ApplyItem{{ID: position.ID, Source: token, Ordinal: 1, SourceDocument: 1, SourceItem: 1, ContentDigest: hex.EncodeToString(mac[:]), Resource: resource}}})
	var problem *cpra.Error
	if !errors.As(err, &problem) || calls.Load() != 1 {
		t.Fatal("drift rejection was hidden or retried", err)
	}
}
