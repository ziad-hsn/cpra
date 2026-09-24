package secureconfig

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const remoteResponseLimit = 256 << 10

var (
	ErrRemoteConfiguration       = errors.New("secureconfig: invalid remote wrapping configuration")
	ErrRemoteUnavailable         = errors.New("secureconfig: wrapping backend unavailable")
	ErrRemoteResponse            = errors.New("secureconfig: invalid wrapping backend response")
	ErrRemoteResponseTooLarge    = errors.New("secureconfig: wrapping backend response exceeds limit")
	ErrRemoteKeyUnsupported      = errors.New("secureconfig: unsupported remote wrapping key")
	ErrAssociatedDataUnsupported = errors.New("secureconfig: wrapping backend did not verify associated data")
)

func remoteTimeout(value time.Duration) (time.Duration, error) {
	if value == 0 {
		return 10 * time.Second, nil
	}
	if value < 0 || value > 5*time.Minute {
		return 0, ErrRemoteConfiguration
	}
	return value, nil
}

func privateWrappingHTTPClient(source *http.Client, timeout time.Duration) (*http.Client, error) {
	var client http.Client
	if source != nil {
		client = *source
	}
	if client.Transport == nil {
		client.Transport = http.DefaultTransport
	}
	owned := false
	if transport, ok := client.Transport.(*http.Transport); ok {
		if transport == nil {
			return nil, ErrRemoteConfiguration
		}
		transport = transport.Clone()
		if transport.TLSClientConfig != nil {
			if transport.TLSClientConfig.InsecureSkipVerify {
				return nil, ErrRemoteConfiguration
			}
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
			if transport.TLSClientConfig.MaxVersion != 0 && transport.TLSClientConfig.MaxVersion < tls.VersionTLS12 {
				return nil, ErrRemoteConfiguration
			}
			if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
				transport.TLSClientConfig.MinVersion = tls.VersionTLS12
			}
		}
		client.Transport = transport
		owned = true
	}
	client.Transport = &boundedWrappingTransport{base: client.Transport, owned: owned}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout == 0 || client.Timeout > timeout {
		client.Timeout = timeout
	}
	return &client, nil
}

// Bound bytes before the SDK/JSON decoder can allocate from an oversized body.
// Only a cloned standard transport is owned; custom transports remain borrowed.
type boundedWrappingTransport struct {
	base  http.RoundTripper
	owned bool
}

func (transport *boundedWrappingTransport) CloseIdleConnections() {
	if transport.owned {
		if closer, ok := transport.base.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

func (transport *boundedWrappingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, ErrRemoteResponse
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, remoteResponseLimit+1))
	if err != nil {
		clear(body)
		return nil, err
	}
	if len(body) > remoteResponseLimit {
		clear(body)
		return nil, ErrRemoteResponseTooLarge
	}
	response.Body = &clearingResponseBody{Reader: bytes.NewReader(body), data: body}
	return response, nil
}

type clearingResponseBody struct {
	*bytes.Reader
	data []byte
}

func (body *clearingResponseBody) Close() error { clear(body.data); body.data = nil; return nil }

func wrappingOrigin(value string) (*url.URL, error) {
	origin, err := url.Parse(value)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.Opaque != "" || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") || origin.RawPath != "" || origin.ForceQuery {
		return nil, ErrRemoteConfiguration
	}
	origin.Host = strings.ToLower(origin.Host)
	origin.Path = ""
	return origin, nil
}

func wrappingPath(value string, nested bool) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	parts := strings.Split(value, "/")
	if !nested && len(parts) != 1 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
				return false
			}
		}
	}
	return true
}

func remoteOperationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrRemoteResponseTooLarge) {
		return ErrRemoteResponseTooLarge
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return ErrRemoteUnavailable
}
