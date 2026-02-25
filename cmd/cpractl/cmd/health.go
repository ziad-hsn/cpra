package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"cpra/cmd/cpractl/client"
)

func init() {
	healthCmd := &cobra.Command{
		Use:   "health",
		Short: "Check CPRA server health",
	}

	liveCmd := &cobra.Command{
		Use:   "live",
		Short: "Liveness probe (/healthz)",
		RunE:  runHealthLive,
	}

	readyCmd := &cobra.Command{
		Use:   "ready",
		Short: "Readiness probe (/readyz)",
		RunE:  runHealthReady,
	}

	healthCmd.AddCommand(liveCmd, readyCmd)
	rootCmd.AddCommand(healthCmd)
}

func runHealthLive(cmd *cobra.Command, args []string) error {
	return checkHealth("liveness", "/healthz")
}

func runHealthReady(cmd *cobra.Command, args []string) error {
	return checkHealth("readiness", "/readyz")
}

type rootInfo struct {
	Health struct {
		Liveness  string `json:"liveness"`
		Readiness string `json:"readiness"`
	} `json:"health"`
}

func checkHealth(kind, fallbackPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), viper.GetDuration("timeout"))
	defer cancel()
	cli, err := client.New()
	if err != nil {
		return err
	}

	path := fallbackPath
	if resolved, err := resolveHealthPath(ctx, cli, kind); err == nil && resolved != "" {
		path = resolved
	}
	return checkEndpoint(ctx, cli, path)
}

func resolveHealthPath(ctx context.Context, cli *client.Client, kind string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cli.BaseURL()+"/", nil)
	if err != nil {
		return "", err
	}
	resp, err := cli.HTTP().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var info rootInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return "", err
	}

	switch kind {
	case "liveness":
		if info.Health.Liveness == "" {
			return "", fmt.Errorf("liveness path missing")
		}
		return normalizePath(info.Health.Liveness), nil
	case "readiness":
		if info.Health.Readiness == "" {
			return "", fmt.Errorf("readiness path missing")
		}
		return normalizePath(info.Health.Readiness), nil
	default:
		return "", fmt.Errorf("unknown health kind: %s", kind)
	}
}

func checkEndpoint(ctx context.Context, cli *client.Client, path string) error {
	path = normalizePath(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cli.BaseURL()+path, nil)
	if err != nil {
		return err
	}

	resp, err := cli.HTTP().Do(req)
	if err != nil {
		color.Red("FAIL")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		color.Green("OK")
		fmt.Println(string(body))
		return nil
	}

	color.Red("FAIL")
	fmt.Println(string(body))
	os.Exit(1)
	return nil
}

func normalizePath(path string) string {
	if path == "" {
		return "/"
	}
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}
