//go:build postgres

package jobs

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

func TestPostgresAccessDiagnostics(t *testing.T) {
	for _, tc := range []struct{ code, want string }{
		{"28P01", "authentication_failed"}, {"28000", "authentication_failed"}, {"42501", "authorization_failed"},
	} {
		assertCheckAccessFailure(t, postgresCheckFailure, &pgconn.PgError{Code: tc.code, Message: "private-provider-detail", Detail: "private-provider-detail"}, tc.want)
	}
	for _, err := range []error{errors.New("SQLSTATE 28P01: private-provider-detail"), &pgconn.PgError{Code: "99999", Message: "password authentication failed"}} {
		if FailureCode(postgresCheckFailure(err)) != "operation_failed" {
			t.Fatal("unrecognized error interpreted as access rejection")
		}
	}
}

func TestPostgresAccessDiagnosticProtocolPath(t *testing.T) {
	addr, calls := checkFailureReceiver(t, func(conn net.Conn) error {
		var header [4]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return err
		}
		length := binary.BigEndian.Uint32(header[:])
		if length < 8 || length > 4096 {
			return errors.New("unexpected startup packet size")
		}
		if _, err := io.CopyN(io.Discard, conn, int64(length)-4); err != nil {
			return err
		}
		packet, err := (&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "private-provider-detail"}).Encode(nil)
		if err != nil {
			return err
		}
		_, err = conn.Write(packet)
		return err
	})
	job := &PulsePostgresJob{ConnString: "postgres://fixture:wrong@" + addr + "/db?sslmode=disable", Timeout: 2 * time.Second}
	err := job.Execute().Err
	if FailureCode(err) != "authentication_failed" || strings.Contains(err.Error(), "private-provider-detail") || calls.Load() != 1 {
		t.Fatalf("production PostgreSQL path: code=%q connections=%d", FailureCode(err), calls.Load())
	}
}
