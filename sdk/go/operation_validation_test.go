package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func validationResponseFixture(t *testing.T) api.ValidationResultPage {
	t.Helper()
	var p api.ValidationResultPage
	if err := json.Unmarshal(inventoryResponse(t, "ValidationResultPage"), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestValidationWaitPendingCancellationRetainsHandleTLS(t *testing.T) {
	var reads, mutations atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
		}
		reads.Add(1)
		if r.URL.Path != "/api/v2/operations/m/validation" || r.URL.RawQuery != "limit=100" || r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Error("unexpected validation request")
		}
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-Request-ID", "read-receipt")
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(api.Problem{Code: "validationPending", OperationID: "m"})
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, AuthToken: "scoped-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err := c.Operations.WaitValidation(ctx, "m")
	if !errors.Is(err, context.DeadlineExceeded) || r == nil || r.OperationID != "m" || r.StatusCode != 409 || r.RequestID != "read-receipt" || reads.Load() != 1 || mutations.Load() != 0 {
		t.Fatal("pending wait changed identity or submitted work", err, reads.Load(), mutations.Load())
	}
}
func TestValidationWaitReadsKnownVerdictsAndTerminalProblems(t *testing.T) {
	for _, mode := range []string{"valid", "invalid", "summary", "validationInterrupted", "validationCanceled", "validationExpired", "historyUnavailable"} {
		t.Run(mode, func(t *testing.T) {
			p := validationResponseFixture(t)
			if mode == "invalid" || mode == "summary" {
				p.Summary.Valid = false
				p.Summary.Issue = "invalidGraph"
				p.Summary.PlanID = ""
				p.Summary.PlanDigest = ""
			}
			if mode == "summary" {
				p.ItemCount = 10001
				p.Summary.SummaryOnly = true
				p.Summary.Issue = "validationLimit"
				p.Summary.Count = 0
				p.Items = []api.ValidationResultItem{}
			}
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" {
					t.Error("wait mutated")
				}
				if strings.HasPrefix(mode, "validation") || mode == "historyUnavailable" {
					w.WriteHeader(409)
					_ = json.NewEncoder(w).Encode(api.Problem{Code: mode})
					return
				}
				_ = json.NewEncoder(w).Encode(p)
			}, nil)
			r, err := c.Operations.WaitValidation(context.Background(), "m")
			terminal := strings.HasPrefix(mode, "validation") || mode == "historyUnavailable"
			if (err != nil) != terminal || r == nil || r.OperationID != "m" || calls != 1 {
				t.Fatal("wait lost exact read result", err, calls)
			}
			if !terminal && r.Data.Summary.Valid != (mode == "valid") {
				t.Fatal("pending/rejection conflated")
			}
		})
	}
}
func TestValidationReadBoundsOptionsAndMalformedPages(t *testing.T) {
	changes := map[string]func(*api.ValidationResultPage){
		"wrong-operation":    func(p *api.ValidationResultPage) { p.OperationID = "other" },
		"bad-count":          func(p *api.ValidationResultPage) { p.ItemCount = 2 },
		"missing-plan":       func(p *api.ValidationResultPage) { p.Summary.PlanID = "" },
		"false-with-plan":    func(p *api.ValidationResultPage) { p.Summary.Valid = false; p.Summary.Issue = "invalidGraph" },
		"summary-truncation": func(p *api.ValidationResultPage) { p.Summary.SummaryOnly = true },
		"source-path":        func(p *api.ValidationResultPage) { p.Items[0].Source = "/private/source.yaml" },
		"source-zero":        func(p *api.ValidationResultPage) { p.Items[0].Source = "source.00000000000000000000" },
		"negative-position":  func(p *api.ValidationResultPage) { p.Items[0].SourceItem = -1 },
		"missing-uid-pair":   func(p *api.ValidationResultPage) { p.Items[0].UID = "original" },
		"empty-nonterminal":  func(p *api.ValidationResultPage) { p.Items = nil; p.NextCursor = "more" },
		"first-gap":          func(p *api.ValidationResultPage) { p.ItemCount = 2; p.Summary.Count = 2; p.Items[0].Ordinal = 2 },
		"expiry":             func(p *api.ValidationResultPage) { p.Summary.ExpiresAt = p.Summary.FinalizedAt },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := validationResponseFixture(t)
			change(&p)
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(p) }, nil)
			if _, err := c.Operations.Validation(context.Background(), "m", ValidationPageOptions{}); err == nil {
				t.Fatal("invalid result accepted")
			}
		})
	}
	t.Run("body-limit", func(t *testing.T) {
		c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat(" ", 4<<20) + "{}"))
		}, nil)
		if _, err := c.Operations.Validation(context.Background(), "m", ValidationPageOptions{}); !errors.Is(err, ErrResponseTooLarge) {
			t.Fatal(err)
		}
	})
	t.Run("options", func(t *testing.T) {
		calls := 0
		c := fixture(t, func(http.ResponseWriter, *http.Request) { calls++ }, nil)
		for _, o := range []ValidationPageOptions{{Limit: -1}, {Limit: 501}, {Cursor: strings.Repeat("x", 4097)}} {
			if _, err := c.Operations.Validation(context.Background(), "m", o); err == nil {
				t.Fatal("invalid options accepted")
			}
		}
		if calls != 0 {
			t.Fatal("bad request submitted")
		}
	})
}
func TestValidationItemsPinsSummaryAndContiguousRows(t *testing.T) {
	for _, mode := range []string{"valid", "changed-result", "changed-source-count", "gap", "timezone"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				p := validationResponseFixture(t)
				p.ItemCount = 2
				p.Summary.Count = 2
				if calls == 1 {
					if r.URL.RawQuery != "limit=1" {
						t.Error("first page sent an empty or unexpected cursor")
					}
					p.NextCursor = "next"
				} else {
					p.Items[0].Ordinal = 2
					p.Items[0].SourceItem = 2
					p.Items[0].ID = "second"
					if r.URL.RawQuery != "cursor=next&limit=1" {
						t.Error("cursor changed")
					}
				}
				if calls == 2 {
					switch mode {
					case "changed-result":
						p.Summary.ResultID = "replacement"
					case "changed-source-count":
						p.ItemCount = 3
						p.Summary.Count = 3
						p.NextCursor = "more"
					case "gap":
						p.Items[0].Ordinal = 1
					case "timezone":
						p.Summary.FinalizedAt = p.Summary.FinalizedAt.In(time.FixedZone("offset", 3600))
						p.Summary.ExpiresAt = p.Summary.ExpiresAt.In(time.FixedZone("offset", 3600))
					}
				}
				_ = json.NewEncoder(w).Encode(p)
			}, nil)
			it := c.Operations.ValidationItems("m", ValidationPageOptions{Limit: 1})
			if calls != 0 {
				t.Fatal("iterator eagerly fetched")
			}
			n := 0
			for it.Next(context.Background()) {
				n++
			}
			valid := mode == "valid" || mode == "timezone"
			if valid && (it.Err() != nil || n != 2) || !valid && (it.Err() == nil || n != 1) || calls != 2 {
				t.Fatal("iterator failed snapshot/order boundary", n, calls, it.Err())
			}
		})
	}
}
func TestValidateRequiresAdmissionReceiptAndNeverRetries(t *testing.T) {
	for _, status := range []int{200, 202, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" {
					t.Error("not admission")
				}
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(api.Operation{ID: "m", State: "validating", ContentDigest: "original"})
			}, func(c *Config) { c.ReadAttempts = 3 })
			_, err := c.Operations.Validate(context.Background(), "m")
			if (err == nil) != (status == 202) || calls != 1 {
				t.Fatal("invalid admission or retry", err, calls)
			}
			if status != 202 && !errors.Is(err, ErrAmbiguous) {
				t.Fatal("uncertain mutation reported definitive", err)
			}
		})
	}
}

func TestValidationPendingRequiresServerIdentityAndHonorsLongDelay(t *testing.T) {
	for _, tc := range []struct {
		name, header, problem, delay string
		ok                           bool
	}{{"header", "m", "", "7200", true}, {"problem", "", "m", "7200", true}, {"missing", "", "", "5", false}, {"wrong-header", "other", "m", "5", false}, {"wrong-problem", "m", "other", "5", false}, {"unsupported-delay", "m", "", "86401", false}, {"overflow", "m", "", "99999999999999999999999", false}, {"bad-delay", "m", "", "canary-private-value", false}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("X-Operation-ID", tc.header)
				w.Header().Set("Retry-After", tc.delay)
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(api.Problem{Code: "validationPending", OperationID: tc.problem})
			}, nil)
			r, err := c.Operations.Validation(context.Background(), "m", ValidationPageOptions{})
			var e *Error
			pending := errors.As(err, &e) && e.Problem.Code == "validationPending"
			if pending != tc.ok || calls != 1 {
				t.Fatal("unconfirmed pending or altered retry interval", err, calls)
			}
			if tc.ok && (r == nil || r.RetryAfter != 2*time.Hour) {
				t.Fatal("long interval was shortened")
			}
			if err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatal("server interval leaked into diagnostic")
			}
		})
	}
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if d, err := validationRetryDelay(now.Add(2*time.Hour).Format(http.TimeFormat), now); err != nil || d != 2*time.Hour {
		t.Fatal(d, err)
	}
}
func TestValidationUnknownObservationsRemainReadable(t *testing.T) {
	p := validationResponseFixture(t)
	p.IdentityFormat = "future-format"
	p.Items[0].Change = "future-change"
	p.Items[0].Kind = "FutureKind"
	p.Items[0].Issue = "future-observation"
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(p) }, nil)
	got, err := c.Operations.Validation(context.Background(), "m", ValidationPageOptions{})
	if err != nil || got.Data.Items[0].Change != "future-change" {
		t.Fatal("unknown read observation rejected", err)
	}
}
func TestValidationResponseRejectsMissingAndNullRequiredValues(t *testing.T) {
	original := inventoryResponse(t, "ValidationResultPage")
	for _, field := range []string{"operationID", "itemCount", "items", "summary.valid", "summary.summaryOnly", "summary.count", "summary.resultID"} {
		for _, null := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/missing", true: "/null"}[null], func(t *testing.T) {
				var body map[string]any
				_ = json.Unmarshal(original, &body)
				target := body
				key := field
				if strings.HasPrefix(field, "summary.") {
					target = body["summary"].(map[string]any)
					key = strings.TrimPrefix(field, "summary.")
				}
				if null {
					target[key] = nil
				} else {
					delete(target, key)
				}
				c := fixture(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body) }, nil)
				if _, err := c.Operations.Validation(context.Background(), "m", ValidationPageOptions{}); err == nil {
					t.Fatal("missing or null observation became zero success")
				}
			})
		}
	}
}
