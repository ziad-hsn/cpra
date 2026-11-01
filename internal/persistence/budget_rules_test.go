package persistence

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestEncodedBoundIncludesMaximumRecoveryRules(t *testing.T) {
	for _, count := range []int{1, 20, 22, 32} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			rules := make([]api.RulePredicate, count)
			targets := make([]RecoveryRuleTarget, count)
			for i := range rules {
				id := fmt.Sprintf("status-%d", 400+i)
				rules[i] = api.RulePredicate{ID: id, Match: api.FailureMatch{Codes: []string{"unexpected_http_status"}, HTTPStatuses: []int{400 + i}}}
				targets[i] = RecoveryRuleTarget{RuleID: id, Driver: "webhook", Binding: identity(id), Budget: time.Minute}
			}
			policy, err := NewRecoveryRules("http", "", "1", rules, targets)
			if err != nil {
				t.Fatal(err)
			}
			m := ruleMonitor(t, 1)
			m.Policy.RecoveryRules = policy
			m.Name = "escaped <>& name"
			command := Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: policyTime()}
			bound, err := CommandEncodedBound(command)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(command)
			if err != nil || len(wire) > bound {
				t.Fatalf("encoded=%d bound=%d err=%v", len(wire), bound, err)
			}
			s := openCatalogMemory(t)
			result := submit(t, s, command)[0]
			if result.Err != nil || result.Monitor == nil || result.Monitor.Policy.RecoveryRules.Digest() != policy.Digest() {
				t.Fatal("rule policy did not commit", result.Err)
			}
			oversized := make([]Command, maxCommitBytes/len(wire)+1)
			for i := range oversized {
				oversized[i] = command
			}
			if _, err := encodedBound(envelope{Version: RecoveryRulesFormatVersion, Commands: oversized}); err == nil {
				t.Fatal("oversized repeated policy batch accepted")
			}
		})
	}
}

type unaccountedJSON struct{}

func (unaccountedJSON) MarshalJSON() ([]byte, error) {
	panic("must not execute custom encoders before quota admission")
}

func TestEncodedBoundRejectsUnknownCustomEncoder(t *testing.T) {
	if _, err := encodedBound(unaccountedJSON{}); err == nil {
		t.Fatal("custom JSON silently undercounted")
	}
}
