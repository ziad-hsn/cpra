package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMonitorTLSSettings(t *testing.T) {
	for _, test := range []struct {
		driver, config string
		valid          bool
	}{
		{"redis", `{"addr":"db:6379","tls":true}`, true},
		{"redis", `{"addr":"db:6379","tls":true,"insecureSkipVerify":true}`, true},
		{"redis", `{"addr":"db:6379","insecureSkipVerify":true}`, false},
		{"kafka", `{"brokers":["db:9093"],"tls":false,"caFile":"ca.pem"}`, false},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=true","tls":true}`, true},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=TRUE","tls":true}`, true},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=preferred","tls":true}`, false},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=true&allowFallbackToPlaintext=true","tls":true}`, false},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=true&allowFallbackToPlaintext=false","tls":true}`, true},
		{"mongo", `{"uri":"mongodb://db/name?tls=true&TLS=false","tls":true}`, false},
		{"postgres", `{"dsn":"host=db sslmode=require SSLMODE=verify-full","tls":true}`, false},
		{"redis", `{"addr":"/tmp/db.sock","tls":true}`, false},
		{"redis", `{"addr":"/tmp/db.sock"}`, true},
		{"redis", `{}`, true},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=skip-verify","tls":true}`, false},
		{"mysql", `{"dsn":"user:private-marker@tcp(db:3306)/name?tls=skip-verify","tls":true,"insecureSkipVerify":true}`, true},
		{"postgres", `{"dsn":"postgres://user:private-marker@db/name?sslmode=disable","tls":true}`, false},
		{"postgres", `{"dsn":"host=db password='private-marker sslmode=disable' sslmode=verify-full","tls":true}`, true},
		{"postgres", `{"dsn":"host=db password='private-marker' sslmode='disable'","tls":true}`, false},
		{"postgres", `{"dsn":"host=db sslmode=verify-full sslmode=require","tls":true}`, false},
		{"postgres", `{"host":"db","sslmode":"prefer","tls":true}`, false},
		{"postgres", `{"host":"db","sslmode":"require","tls":true,"insecureSkipVerify":true}`, true},
		{"mongo", `{"uri":"mongodb://user:private-marker@one:27017,two:27017/name?tls=true","tls":true}`, true},
		{"mongo", `{"uri":"mongodb://db/name?tlsInsecure=true","tls":true}`, false},
		{"mongo", `{"uri":"mongodb://db/name?ssl=false","tls":true}`, false},
		{"rabbitmq", `{"url":"amqps://user:private-marker@db/name","tls":true,"caFile":"ca.pem"}`, true},
		{"rabbitmq", `{"url":"amqp://user:private-marker@db/name","tls":true}`, false},
		{"rabbitmq", `{"url":"amqps://user:private-marker@db/name","tls":false}`, false},
		{"http", `{"url":"http://db","insecureSkipVerify":true}`, false},
		{"http", `{"url":"https://db","insecureSkipVerify":false,"caFile":"ca.pem","serverName":"db.example"}`, true},
		{"grpc", `{"host":"db","port":443,"mode":"health","insecureSkipVerify":true}`, true},
		{"grpc", `{"host":"db","port":443,"mode":"tcp","insecureSkipVerify":true}`, false},
		{"grpc", `{"host":"db","port":443,"mode":"health","allowInsecure":true,"insecureSkipVerify":true}`, false},
		{"tcp", `{"host":"db","port":443,"insecureSkipVerify":true}`, false},
	} {
		t.Run(test.driver+"/"+test.config, func(t *testing.T) {
			err := ValidateDriverSettings("check", DriverConfig{Type: test.driver, Config: json.RawMessage(test.config)})
			if (err == nil) != test.valid {
				t.Fatal("unexpected validation result", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("validation exposed a credential")
			}
		})
	}
}

func TestTLSDriverModelsPreserveExplicitFalse(t *testing.T) {
	for _, driver := range []string{"redis", "postgres", "mysql", "mongo", "rabbitmq", "kafka"} {
		input := json.RawMessage(`{"tls":true,"insecure_skip_verify":false,"ca_file":"root.pem","server_name":"db.example"}`)
		normalized, err := NormalizeManifestDriver("check", driver, input)
		if err != nil {
			t.Fatal(driver, err)
		}
		var values map[string]any
		if json.Unmarshal(normalized, &values) != nil || values["insecureSkipVerify"] != false || values["tls"] != true || values["caFile"] != "root.pem" {
			t.Fatal("TLS fields lost", driver)
		}
	}
}
