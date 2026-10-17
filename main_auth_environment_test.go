package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestAuthEnvironmentDoesNotEnterFlagDefaults(t *testing.T) {
	for _, test := range []struct {
		name        string
		args        []string
		token, file string
	}{
		{"environment", nil, "environment-marker", "environment-file"},
		{"explicit", []string{"-web.auth=explicit-token", "-web.auth-file=explicit-file"}, "explicit-token", "explicit-file"},
		{"explicit empty", []string{"-web.auth=", "-web.auth-file="}, "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var o runOptions
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			flags.StringVar(&o.webAuth, "web.auth", "", "API token")
			flags.StringVar(&o.webAuthFile, "web.auth-file", "", "API token file")
			if err := flags.Parse(test.args); err != nil {
				t.Fatal(err)
			}
			resolveAuthEnvironment(&o, flags, func(name string) string {
				if name == "CPRA_AUTH_TOKEN" {
					return "environment-marker"
				}
				return "environment-file"
			})
			if o.webAuth != test.token || o.webAuthFile != test.file {
				t.Fatal("unexpected authentication source precedence")
			}
			var output bytes.Buffer
			flags.SetOutput(&output)
			flags.PrintDefaults()
			_ = flags.Parse([]string{"-unknown"})
			for _, secret := range []string{"environment-marker", "explicit-token", "environment-file", "explicit-file"} {
				if strings.Contains(output.String(), secret) {
					t.Fatal("authentication value entered usage/error output")
				}
			}
		})
	}
}
