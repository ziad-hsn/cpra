package api

import (
	"fmt"
	"testing"
)

func BenchmarkRecoveryRuleMatch(b *testing.B) {
	for _, count := range []int{1, 32} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			rules := make([]RecoveryRule, count)
			for i := range rules {
				rules[i] = testRecoveryRule(fmt.Sprintf("status-%d", i), "unexpected_http_status")
				rules[i].Match.HTTPStatuses = []int{400 + i}
			}
			policy, _, err := CompileRecoveryRules(rulesHTTPCheck(), testRecoveryPolicy(rules...))
			if err != nil {
				b.Fatal(err)
			}
			failure, err := CanonicalFailure("1", "unexpected_http_status", "response")
			if err != nil {
				b.Fatal(err)
			}
			failure.HTTPStatus = Pointer(400 + count - 1)
			b.ReportAllocs()
			for b.Loop() {
				if got := policy.Match(failure); got.Index != count-1 || !got.Intervention {
					b.Fatal(got)
				}
			}
			b.ReportMetric(float64(policy.MemoryBytes()), "policy-bytes")
		})
	}
}
