//go:build windows

package jobs

import "testing"

func TestCheckTLSRejectsDeviceNamespaces(t *testing.T) {
	for _, path := range []string{`\\.\pipe\cpra-certificate`, `\\?\C:\certificate.pem`, `//./pipe/cpra-certificate`, `//?/C:/certificate.pem`} {
		file, err := openCheckCertificate(path)
		if file != nil {
			_ = file.Close()
		}
		if err == nil {
			t.Fatal("device namespace admitted")
		}
	}
}
