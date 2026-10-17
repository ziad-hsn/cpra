//go:build mysql

package jobs

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestMySQLRejectsLocalFileAccessBeforeConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	for _, value := range []string{"true", "TRUE", "True", "1"} {
		for _, suffix := range []string{"", "&charset=utf8mb4", "&sql_mode=%27ANSI%27"} {
			dsn := "canary:private-marker@tcp(" + ln.Addr().String() + ")/db?allowAllFiles=" + value + suffix
			if _, err := newPulseMySQLJob(&manifest.PulseMySQLConfig{DSN: dsn}, time.Second, ecs.Entity{}); err == nil || strings.Contains(err.Error(), "private-marker") {
				t.Fatal("unsafe constructor accepted or leaked DSN", err)
			}
			result := (&PulseMySQLJob{DSN: dsn, Timeout: time.Second}).Execute()
			if result.Err == nil || strings.Contains(result.Err.Error(), "private-marker") {
				t.Fatal("direct execution bypassed DSN policy", result.Err)
			}
		}
	}
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Millisecond))
	if conn, err := ln.Accept(); err == nil {
		conn.Close()
		t.Fatal("rejected DSN opened a connection")
	}
}

func TestMySQLParsedBooleanAuthorityAndSafeDiscreteCredentials(t *testing.T) {
	for _, value := range []string{"false", "FALSE", "False", "0"} {
		cfg, err := mysqlCheckConfig("user:private-marker@tcp(localhost:3306)/db?allowAllFiles=" + value)
		if err != nil || cfg.AllowAllFiles {
			t.Fatal("safe driver boolean rejected", err)
		}
	}
	cfg, err := mysqlCheckConfig(mysqlDSN(&manifest.PulseMySQLConfig{Host: "::1", User: "fixture", Password: "special:@/password", Database: "name/with?syntax"}))
	if err != nil || cfg.Passwd != "special:@/password" || cfg.DBName != "name/with?syntax" || cfg.Addr != "[::1]:3306" {
		t.Fatal("discrete fields were reinterpreted as DSN options", err)
	}
}
