//go:build redis && postgres && mysql && mongo && rabbitmq && kafka

package jobs

import (
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestCheckTLSRedisPing(t *testing.T) {
	certificate, ca := checkTestCertificate(t, false)
	server, err := miniredis.RunTLS(&tls.Config{Certificates: []tls.Certificate{certificate}})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, test := range []struct {
		ca, name   string
		skip, want bool
	}{{ca, "db.example", false, true}, {ca, "wrong.example", false, false}, {"", "wrong.example", true, true}, {"", "db.example", false, false}} {
		job, err := newPulseRedisJob(&manifest.PulseRedisConfig{Addr: server.Addr(), TLS: api.Pointer(true), CAFile: test.ca, ServerName: test.name, InsecureSkipVerify: api.Pointer(test.skip)}, 300*time.Millisecond, ecs.Entity{})
		if err != nil {
			t.Fatal(err)
		}
		if result := job.Execute(); (result.Err == nil) != test.want {
			t.Fatal("wrong Redis TLS result", result.Err)
		}
	}
}

// These fixtures independently observe production-driver TLS handshakes. They
// deliberately stop before authentication, so they do not certify database or
// broker operations. Existing operation fixtures remain separate evidence.
func TestCheckTLSDatabaseAndBrokerTransports(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql", "mongo", "rabbitmq", "kafka"} {
		for _, test := range []struct {
			name                              string
			expired, trust, wrong, skip, want bool
		}{
			{"trusted", false, true, false, false, true},
			{"untrusted", false, false, false, false, false},
			{"wrong-host", false, true, true, false, false},
			{"expired", true, true, false, false, false},
			{"skip-expired-wrong-host", true, false, true, true, true},
		} {
			t.Run(driver+"/"+test.name, func(t *testing.T) {
				certificate, ca := checkTestCertificate(t, test.expired)
				address, handshakes := serveCheckTLSHandshake(t, driver, certificate)
				if !test.trust {
					ca = ""
				}
				name := "db.example"
				if test.wrong {
					name = "wrong.example"
				}
				var config manifest.PulseConfig
				enabled, skip := api.Pointer(true), api.Pointer(test.skip)
				switch driver {
				case "postgres":
					config = &manifest.PulsePostgresConfig{Host: "127.0.0.1", Port: address.Port, User: "fixture", Database: "fixture", TLS: enabled, InsecureSkipVerify: skip, CAFile: ca, ServerName: name}
				case "mysql":
					config = &manifest.PulseMySQLConfig{Host: "127.0.0.1", Port: address.Port, User: "fixture", Database: "fixture", TLS: enabled, InsecureSkipVerify: skip, CAFile: ca, ServerName: name}
				case "mongo":
					config = &manifest.PulseMongoConfig{URI: "mongodb://" + address.String() + "/?directConnection=true", TLS: enabled, InsecureSkipVerify: skip, CAFile: ca, ServerName: name}
				case "rabbitmq":
					config = &manifest.PulseRabbitMQConfig{URL: "amqps://fixture:fixture@" + address.String() + "/", TLS: enabled, InsecureSkipVerify: skip, CAFile: ca, ServerName: name}
				case "kafka":
					config = &manifest.PulseKafkaConfig{Brokers: []string{address.String()}, TLS: enabled, InsecureSkipVerify: skip, CAFile: ca, ServerName: name}
				}
				job, err := CreatePulseJob(manifest.Pulse{Type: driver, Config: config, Timeout: 200 * time.Millisecond}, ecs.Entity{})
				if err != nil {
					t.Fatal(err)
				}
				if job.Execute().Err == nil {
					t.Fatal("handshake-only fixture unexpectedly completed an operation")
				}
				if (handshakes.Load() > 0) != test.want {
					t.Fatal("driver did not enforce the selected TLS policy", handshakes.Load())
				}
			})
		}
	}
}

func serveCheckTLSHandshake(t *testing.T, protocol string, certificate tls.Certificate) (*net.TCPAddr, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	var completed atomic.Int64
	workers.Go(func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(time.Second))
				switch protocol {
				case "postgres":
					var request [8]byte
					if _, err := io.ReadFull(connection, request[:]); err != nil || binary.BigEndian.Uint32(request[:4]) != 8 || binary.BigEndian.Uint32(request[4:]) != 80877103 {
						return
					}
					if _, err := connection.Write([]byte{'S'}); err != nil {
						return
					}
				case "mysql":
					if !startMySQLFixtureTLS(connection) {
						return
					}
				}
				client := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}})
				if client.Handshake() == nil {
					completed.Add(1)
					var marker [1]byte
					_, _ = client.Read(marker[:])
				}
			})
		}
	})
	t.Cleanup(func() { listener.Close(); workers.Wait() })
	return listener.Addr().(*net.TCPAddr), &completed
}

func startMySQLFixtureTLS(connection net.Conn) bool {
	const capabilities uint32 = 1<<9 | 1<<11 | 1<<15 | 1<<19
	payload := []byte{10}
	payload = append(payload, []byte("8.0.0\x00")...)
	payload = binary.LittleEndian.AppendUint32(payload, 1)
	payload = append(payload, []byte("12345678\x00")...)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(capabilities&0xffff))
	payload = append(payload, 45)
	payload = binary.LittleEndian.AppendUint16(payload, 2)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(capabilities>>16))
	payload = append(payload, 21)
	payload = append(payload, make([]byte, 10)...)
	payload = append(payload, []byte("abcdefghijkl\x00mysql_native_password\x00")...)
	packet := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), 0}
	if _, err := connection.Write(append(packet, payload...)); err != nil {
		return false
	}
	var header [4]byte
	if _, err := io.ReadFull(connection, header[:]); err != nil {
		return false
	}
	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if size != 32 {
		return false
	}
	request := make([]byte, size)
	if _, err := io.ReadFull(connection, request); err != nil {
		return false
	}
	return binary.LittleEndian.Uint32(request)&(1<<11) != 0
}
