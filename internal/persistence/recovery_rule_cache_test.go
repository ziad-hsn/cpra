package persistence

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestRecoveryRulesProgramCacheLimits(t *testing.T) {
	for _, limit := range []string{"entries", "bytes"} {
		t.Run(limit, func(t *testing.T) {
			makePolicy := func(index int) RecoveryRules {
				t.Helper()
				count := 1
				if limit == "bytes" {
					count = api.MaxRecoveryRules
				}
				rules := make([]api.RulePredicate, count)
				for i := range rules {
					id := fmt.Sprintf("policy-%05d-rule-%02d", index, i)
					if limit == "bytes" {
						id += strings.Repeat("x", 63-len(id))
					}
					rules[i] = api.RulePredicate{ID: id, None: true, Match: api.FailureMatch{
						Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{500 + i},
					}}
				}
				policy, err := NewRecoveryRules("http", "", api.FailureTaxonomyVersion, rules, nil)
				if err != nil {
					t.Fatal(err)
				}
				return policy
			}
			var cache recoveryProgramCache
			var retained []RecoveryRules
			var private RecoveryRules
			for index := 0; index <= maxRecoveryProgramCacheEntries; index++ {
				policy := makePolicy(index)
				owned := cache.replace(RecoveryRules{}, policy)
				if owned.data.intern == nil {
					private = owned
					if owned.data != policy.data {
						t.Fatal("uncached policy lost private ownership")
					}
					break
				}
				retained = append(retained, owned)
			}
			if private.IsZero() || cache.bytes > maxRecoveryProgramCacheBytes || len(cache.entries) > maxRecoveryProgramCacheEntries {
				t.Fatal("unique policies exceeded the cache limits")
			}
			if limit == "entries" && len(cache.entries) != maxRecoveryProgramCacheEntries ||
				limit == "bytes" && (len(cache.entries) >= maxRecoveryProgramCacheEntries || private.data.compiled.MemoryBytes() <= maxRecoveryProgramCacheBytes-cache.bytes) {
				t.Fatal("fixture did not reach the selected limit", len(cache.entries), cache.bytes)
			}
			var accounted uint64
			for _, entry := range cache.entries {
				if entry.users != 1 {
					t.Fatal("unique policy shared another entry")
				}
				accounted += entry.bytes
			}
			if accounted != cache.bytes {
				t.Fatal("cache byte accounting differs from retained programs")
			}
			t.Logf("limit=%s entries=%d program_bytes=%d private_program_bytes=%d", limit, len(cache.entries), cache.bytes, private.data.compiled.MemoryBytes())
			// Sharing a retained policy still works at either limit. Removing a
			// private user must not decrement an unrelated cached program.
			shared := cache.replace(RecoveryRules{}, makePolicy(0))
			if shared.data.compiled != retained[0].data.compiled || shared.data.intern.users != 2 || cache.bytes != accounted {
				t.Fatal("saturation prevented sharing or charged the program twice")
			}
			cache.replace(private, RecoveryRules{})
			if cache.bytes != accounted || len(cache.entries) != len(retained) {
				t.Fatal("private release changed cache ownership")
			}
			cache.replace(shared, RecoveryRules{})
			// A frozen snapshot may retain the immutable policy after its last
			// live user leaves the cache. Its match must remain unchanged.
			failure, err := api.CanonicalFailure(api.FailureTaxonomyVersion, "unexpected_http_status", "response")
			if err != nil {
				t.Fatal(err)
			}
			failure.HTTPStatus = api.Pointer(500)
			frozen := retained[0]
			before := frozen.Match(CheckFailure(failure))
			for _, policy := range retained {
				cache.replace(policy, RecoveryRules{})
			}
			if cache.bytes != 0 || len(cache.entries) != 0 || !before.Matched || before.Intervention || frozen.Match(CheckFailure(failure)) != before {
				t.Fatal("last release leaked accounting or invalidated a snapshot")
			}
			// A private fallback can become shared when capacity is available.
			admitted := cache.replace(private, private)
			if admitted.data.intern == nil || admitted.data.intern.users != 1 || admitted.data.compiled != private.data.compiled {
				t.Fatal("private fallback could not join the recovered cache")
			}
			cache.replace(admitted, RecoveryRules{})
			if cache.bytes != 0 || len(cache.entries) != 0 {
				t.Fatal("readmitted program leaked")
			}
		})
	}
}
