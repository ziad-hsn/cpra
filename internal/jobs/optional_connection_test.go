//go:build redis && postgres && mysql

package jobs

import (
	"context"
	"net"
	"testing"
	"time"

	"cpra/internal/loader/schema"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

func TestPostgresDiscreteCredentials(t *testing.T) {
	for _, password := range []string{"", "first second", "first\\second", "first' second"} {
		c := &schema.PulsePostgresConfig{Host: "127.0.0.1", User: "fixture", Password: password, Database: "fixture", SSLMode: "disable"}
		parsed, err := pgx.ParseConfig(postgresConnString(c))
		if err != nil {
			t.Errorf("password %q parse error: %v", password, err)
			continue
		}
		if parsed.Password != password || parsed.Database != c.Database {
			t.Errorf("password %q mapped to %q; db=%q", password, parsed.Password, parsed.Database)
		}
	}
}
func TestMySQLIPv6DiscreteHost(t *testing.T) {
	c := &schema.PulseMySQLConfig{Host: "::1", User: "fixture", Password: "fixture", Database: "fixture"}
	parsed, err := mysql.ParseDSN(mysqlDSN(c))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := net.SplitHostPort(parsed.Addr); err != nil {
		t.Errorf("IPv6 host generated invalid address %q: %v", parsed.Addr, err)
	}
}
func TestRedisOwnerCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j := &PulseRedisJob{Addr: ln.Addr().String(), Timeout: 3 * time.Second}
	j.SetContext(ctx)
	done := make(chan Result, 1)
	go func() { done <- j.Execute() }()
	c := <-accepted
	defer c.Close()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
		return
	case <-time.After(200 * time.Millisecond):
	}
	c.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cleanup failed")
	}
	t.Error("Redis read did not stop within 200ms after owner cancellation; 3s operation deadline remained")
}
