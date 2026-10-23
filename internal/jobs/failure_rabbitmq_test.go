//go:build rabbitmq

package jobs

import (
	"errors"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestRabbitMQAccessDiagnostics(t *testing.T) {
	assertCheckAccessFailure(t, rabbitmqCheckFailure, amqp.ErrCredentials, "authentication_failed")
	assertCheckAccessFailure(t, rabbitmqCheckFailure, amqp.ErrVhost, "authorization_failed")
	assertCheckAccessFailure(t, rabbitmqCheckFailure, &amqp.Error{Code: amqp.AccessRefused, Reason: "private-provider-detail"}, "authorization_failed")
	for _, err := range []error{errors.New("ACCESS_REFUSED private-provider-detail"), amqp.ErrSASL, &amqp.Error{Code: amqp.InternalError, Reason: "access refused"}} {
		if FailureCode(rabbitmqCheckFailure(err)) != "operation_failed" {
			t.Fatal("unrecognized error interpreted as access rejection")
		}
	}
}
