//go:build redis

package jobs

import (
	"context"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

func TestCheckTLSRedisAddressDefaults(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, test := range []struct {
		addr, network, want string
		tls                 bool
		valid               bool
	}{
		{"", "tcp", "localhost:6379", false, true},
		{"", "tcp", "localhost:6379", true, true},
		{"/tmp/fixture.sock", "unix", "/tmp/fixture.sock", false, true},
		{"/tmp/fixture.sock", "", "", true, false},
	} {
		job := &PulseRedisJob{Addr: test.addr, tls: checkTLS{configured: test.tls, enabled: test.tls}}
		options, err := job.clientOptions(ctx)
		if (err == nil) != test.valid {
			t.Fatal("address compatibility changed", test, err)
		}
		if err != nil {
			continue
		}
		client := redis.NewClient(options)
		if client.Options().Addr != test.want || client.Options().Network != test.network {
			t.Fatal("client defaults changed", test)
		}
		_ = client.Close()
	}
}
