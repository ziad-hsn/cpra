package jobs

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"cpra/internal/loader/schema"
	"github.com/mlange-42/ark/ecs"
	"golang.org/x/net/dns/dnsmessage"
)

func TestDNSServerAddress(t *testing.T) {
	for _, tt := range []struct{ server, want string }{
		{"resolver.test", "resolver.test:53"}, {"127.0.0.1", "127.0.0.1:53"}, {"127.0.0.1:15353", "127.0.0.1:15353"},
		{"::1", "[::1]:53"}, {"[::1]", "[::1]:53"}, {"[::1]:15353", "[::1]:15353"}, {"fe80::1%eth0", "[fe80::1%eth0]:53"},
	} {
		t.Run(tt.server, func(t *testing.T) {
			got, err := dnsServerAddress(tt.server)
			if err != nil || got != tt.want {
				t.Errorf("got=%q err=%v want=%q", got, err, tt.want)
			}
		})
	}
	for _, server := range []string{"", ":53", "resolver:", "resolver:0", "resolver:65536", "resolver:dns", "[invalid]", "http://resolver", "bad host"} {
		if _, err := dnsServerAddress(server); err == nil {
			t.Errorf("accepted invalid server %q", server)
		}
	}
}

func TestDNSCustomPortNetwork(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	done := make(chan struct{})
	var questions atomic.Int32
	go func() {
		defer close(done)
		buffer := make([]byte, 4096)
		for {
			n, peer, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			var query dnsmessage.Message
			if err := query.Unpack(buffer[:n]); err != nil {
				t.Error(err)
				return
			}
			response := dnsmessage.Message{Header: dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RecursionAvailable: true}, Questions: query.Questions}
			for _, q := range query.Questions {
				if q.Name.String() != "cpra-fixture.invalid." {
					t.Errorf("unexpected question: %s", q.Name.String())
					continue
				}
				questions.Add(1)
				if q.Type == dnsmessage.TypeA {
					response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, Type: dnsmessage.TypeA, TTL: 1}, Body: &dnsmessage.AResource{A: [4]byte{127, 0, 0, 1}}})
				}
			}
			wire, err := response.Pack()
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := conn.WriteTo(wire, peer); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	job, err := CreatePulseJob(schema.Pulse{Type: "dns", Timeout: time.Second, Config: &schema.PulseDNSConfig{Host: "cpra-fixture.invalid", Server: conn.LocalAddr().String()}}, ecs.Entity{})
	if err != nil {
		t.Fatal(err)
	}
	if r := job.Copy().Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if questions.Load() == 0 {
		t.Fatal("custom DNS server received no questions")
	}
	_ = conn.Close()
	<-done
	if r := job.Execute(); r.Err == nil {
		t.Fatal("closed custom DNS server reported success")
	}
}
