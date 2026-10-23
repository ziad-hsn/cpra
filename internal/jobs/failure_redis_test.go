//go:build redis

package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

type redisProtocolError string

func (e redisProtocolError) Error() string { return string(e) }
func (redisProtocolError) RedisError()     {}

func TestRedisAccessDiagnostics(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"WRONGPASS", "authentication_failed"}, {"NOAUTH", "authentication_failed"}, {"NOPERM", "authorization_failed"},
	} {
		assertCheckAccessFailure(t, redisCheckFailure, redisProtocolError(tc.code+" private-provider-detail"), tc.want)
	}
	for _, err := range []error{errors.New("WRONGPASS private-provider-detail"), redisProtocolError("ERR WRONGPASS private-provider-detail"), redisProtocolError("NOPERMANENT private-provider-detail")} {
		if FailureCode(redisCheckFailure(err)) != "operation_failed" {
			t.Fatal("unrecognized error interpreted as access rejection")
		}
	}
}

func TestRedisAccessDiagnosticProtocolPath(t *testing.T) {
	server := miniredis.RunT(t)
	server.RequireAuth("fixture-correct")
	for _, password := range []string{"", "fixture-wrong"} {
		job := &PulseRedisJob{Addr: server.Addr(), Password: password, Timeout: 2 * time.Second}
		if code := FailureCode(job.Execute().Err); code != "authentication_failed" {
			t.Fatalf("production Redis AUTH path: code=%q", code)
		}
	}
}
