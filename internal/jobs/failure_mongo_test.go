//go:build mongo

package jobs

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/topology"
)

func TestMongoAccessDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		code int32
		want string
	}{
		{18, "authentication_failed"}, {13, "authorization_failed"},
	} {
		assertCheckAccessFailure(t, mongoCheckFailure, mongo.CommandError{Code: tc.code, Message: "private-provider-detail"}, tc.want)
		assertCheckAccessFailure(t, mongoCheckFailure, topology.ConnectionError{Wrapped: driver.Error{Code: tc.code, Message: "private-provider-detail"}}, tc.want)
	}
	for _, err := range []error{errors.New("AuthenticationFailed private-provider-detail"), mongo.CommandError{Code: 9999, Message: "AuthenticationFailed"}} {
		if FailureCode(mongoCheckFailure(err)) != "operation_failed" {
			t.Fatal("unrecognized error interpreted as access rejection")
		}
	}
}
