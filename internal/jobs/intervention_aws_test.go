//go:build aws

package jobs

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestAWSEffectPreservesConfiguredTrustAndSingleAttempt(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_SESSION_TOKEN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ENDPOINT_URL", "AWS_CA_BUNDLE"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "absent-credentials"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "absent-config"))
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-secret")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, status := range []int{200, 500, 0} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method %s", r.Method)
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/xml")
				w.WriteHeader(status)
				if status == 200 {
					_, _ = io.WriteString(w, `<RebootInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>fixture</requestId><return>true</return></RebootInstancesResponse>`)
				} else {
					_, _ = io.WriteString(w, `<Response><Errors><Error><Code>InternalError</Code><Message>fixture</Message></Error></Errors><RequestID>fixture</RequestID></Response>`)
				}
			}))
			defer server.Close()
			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AWS_CA_BUNDLE", ca)
			t.Setenv("AWS_ENDPOINT_URL_EC2", server.URL)
			if status == 0 {
				t.Setenv("AWS_ENDPOINT_URL_EC2", "https://"+refusedTCPAddress(t))
			}
			job := &InterventionAWSJob{Region: "us-east-1", Operation: "reboot-instance", InstanceID: "i-00000000000000000", Timeout: 3 * time.Second}
			result := job.Execute()
			want, count := EffectSucceeded, int32(1)
			if status == 500 {
				want = EffectUnknown
			} else if status == 0 {
				want, count = EffectNotApplied, 0
			}
			if got := ClassifyEffect(result.Err); got.Outcome != want || got.Retryable != (status == 0) || calls.Load() != count {
				t.Fatalf("AWS transport: %+v calls=%d err=%v", got, calls.Load(), result.Err)
			}
		})
	}
}
