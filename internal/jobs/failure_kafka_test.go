//go:build kafka

package jobs

import (
	"errors"
	"testing"

	"github.com/twmb/franz-go/pkg/kerr"
)

func TestKafkaAccessDiagnostics(t *testing.T) {
	assertCheckAccessFailure(t, kafkaCheckFailure, &kerr.Error{Code: kerr.SaslAuthenticationFailed.Code, Description: "private-provider-detail"}, "authentication_failed")
	for _, provider := range []*kerr.Error{kerr.TopicAuthorizationFailed, kerr.GroupAuthorizationFailed, kerr.ClusterAuthorizationFailed, kerr.TransactionalIDAuthorizationFailed, kerr.DelegationTokenAuthorizationFailed} {
		assertCheckAccessFailure(t, kafkaCheckFailure, &kerr.Error{Code: provider.Code, Description: "private-provider-detail"}, "authorization_failed")
	}
	for _, err := range []error{errors.New("SASL_AUTHENTICATION_FAILED private-provider-detail"), kerr.UnsupportedSaslMechanism, &kerr.Error{Code: 9999, Description: "authorization failed"}} {
		if FailureCode(kafkaCheckFailure(err)) != "operation_failed" {
			t.Fatal("unrecognized error interpreted as access rejection")
		}
	}
}
