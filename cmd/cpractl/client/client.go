package client

import (
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/viper"

	"cpra/gen/cpra/v1/cprav1connect"
)

// Client wraps Connect-Go and HTTP clients for interacting with a CPRA server.
type Client struct {
	monitor cprav1connect.MonitorServiceClient
	http    *http.Client
	baseURL string
}

// New creates a new Client using Viper configuration.
func New() (*Client, error) {
	serverURL := viper.GetString("server")
	if serverURL == "" {
		serverURL = "http://localhost:8080"
	}
	serverURL = strings.TrimRight(serverURL, "/")

	timeout := viper.GetDuration("timeout")
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	httpClient := &http.Client{Timeout: timeout}
	if viper.GetBool("insecure") {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true
		httpClient.Transport = transport
	}

	return &Client{
		monitor: cprav1connect.NewMonitorServiceClient(httpClient, serverURL),
		http:    httpClient,
		baseURL: serverURL,
	}, nil
}

// MonitorService returns the generated MonitorService client.
func (c *Client) MonitorService() cprav1connect.MonitorServiceClient {
	return c.monitor
}

// HTTP returns the underlying *http.Client for direct HTTP endpoints.
func (c *Client) HTTP() *http.Client {
	return c.http
}

// BaseURL returns the configured server base URL.
func (c *Client) BaseURL() string {
	return c.baseURL
}
