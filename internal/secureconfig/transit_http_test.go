package secureconfig

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type trackedWrappingBody struct {
	io.Reader
	closed atomic.Bool
}

type borrowedWrappingTransport struct{ closed atomic.Bool }

func (transport *borrowedWrappingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, ErrRemoteUnavailable
}
func (transport *borrowedWrappingTransport) CloseIdleConnections() { transport.closed.Store(true) }

func TestWrappingClientDoesNotCloseBorrowedTransport(t *testing.T) {
	borrowed := &borrowedWrappingTransport{}
	client, err := privateWrappingHTTPClient(&http.Client{Transport: borrowed}, 10e9)
	if err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	if borrowed.closed.Load() {
		t.Fatal("adapter closed caller-owned transport")
	}
}

func (body *trackedWrappingBody) Close() error { body.closed.Store(true); return nil }

func TestWrappingTransportClosesBoundedResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		size int
		want error
	}{{"accepted", 32, nil}, {"ceiling", remoteResponseLimit, nil}, {"overflow", remoteResponseLimit + 1, ErrRemoteResponseTooLarge}} {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedWrappingBody{Reader: strings.NewReader(strings.Repeat("p", test.size))}
			client, err := privateWrappingHTTPClient(&http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header)}, nil
			})}, 10e9)
			if err != nil {
				t.Fatal(err)
			}
			request, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid", nil)
			response, err := client.Transport.RoundTrip(request)
			if !errors.Is(err, test.want) {
				t.Fatalf("expected %v, got %v", test.want, err)
			}
			if !body.closed.Load() {
				t.Fatal("underlying network response leaked")
			}
			if err == nil {
				clearing, ok := response.Body.(*clearingResponseBody)
				if !ok {
					t.Fatal("private response body missing")
				}
				owned := clearing.data
				read, err := io.ReadAll(response.Body)
				if err != nil || len(read) != test.size {
					t.Fatal("valid body truncated")
				}
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(owned, make([]byte, len(owned))) {
					t.Fatal("response buffer not cleared on close")
				}
			}
		})
	}
}

func TestWrappingClientKeepsTLSVerificationAndTokenErrorsPrivate(t *testing.T) {
	for _, tlsConfig := range []*tls.Config{{InsecureSkipVerify: true}, {MaxVersion: tls.VersionTLS11}} {
		if _, err := privateWrappingHTTPClient(&http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}, 10e9); !errors.Is(err, ErrRemoteConfiguration) {
			t.Fatal("unsafe TLS configuration accepted")
		}
	}
	f := newTransitFixture(t, nil)
	opts := f.options()
	opts.TokenSource = func(context.Context) (string, error) { return "", errors.New("fixture-private-token-in-source-error") }
	if _, err := NewTransitWrapper(context.Background(), opts); !errors.Is(err, ErrRemoteUnavailable) || strings.Contains(err.Error(), "fixture-private-token") {
		t.Fatalf("token source error leaked: %v", err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("missing token request reached provider")
	}
	for _, token := range []string{"", "token\r\nInjected: secret", strings.Repeat("t", 8193)} {
		opts.TokenSource = func(context.Context) (string, error) { return token, nil }
		if _, err := NewTransitWrapper(context.Background(), opts); !errors.Is(err, ErrRemoteConfiguration) {
			t.Fatalf("unsafe token accepted: %v", err)
		}
	}
}
