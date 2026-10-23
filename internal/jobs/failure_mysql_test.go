//go:build mysql

package jobs

import (
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestMySQLAccessDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		code uint16
		want string
	}{
		{1045, "authentication_failed"},
		{1044, "authorization_failed"},
		{1142, "authorization_failed"},
		{1143, "authorization_failed"},
		{1227, "authorization_failed"},
	} {
		assertCheckAccessFailure(t, mysqlCheckFailure, &mysql.MySQLError{Number: tc.code, Message: "private-provider-detail"}, tc.want)
	}
	for _, err := range []error{errors.New("Error 1045: private-provider-detail"), &mysql.MySQLError{Number: 9999, Message: "access denied"}} {
		if FailureCode(mysqlCheckFailure(err)) != "operation_failed" {
			t.Fatal("unrecognized error interpreted as access rejection")
		}
	}
}

func TestMySQLAccessDiagnosticProtocolPath(t *testing.T) {
	addr, calls := checkFailureReceiver(t, func(conn net.Conn) error {
		body := []byte{0xff, 0, 0, '#', '2', '8', '0', '0', '0'}
		binary.LittleEndian.PutUint16(body[1:3], 1045)
		body = append(body, []byte("private-provider-detail")...)
		packet := append([]byte{byte(len(body)), 0, 0, 0}, body...)
		_, err := conn.Write(packet)
		return err
	})
	job := &PulseMySQLJob{DSN: "fixture:wrong@tcp(" + addr + ")/db", Timeout: 2 * time.Second}
	err := job.Execute().Err
	if FailureCode(err) != "authentication_failed" || strings.Contains(err.Error(), "private-provider-detail") || calls.Load() != 1 {
		t.Fatalf("production MySQL path: code=%q connections=%d", FailureCode(err), calls.Load())
	}
}
