// Command cpra-fixture serves an in-memory SDK contract fixture on loopback.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/ziad-hsn/cpra/examples/sdk/internal/cprafixture"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"
)

func main() {
	address := flag.String("listen", "127.0.0.1:8090", "loopback listener")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		fmt.Fprintln(os.Stderr, "fixture requires a numeric loopback address")
		os.Exit(1)
	}
	server := &http.Server{Addr: *address, Handler: cprafixture.NewHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(c)
	}()
	fmt.Println("in-memory SDK fixture; no CPRa scheduler, providers, or durability")
	fmt.Println("listen", *address, "public fixture token:", cprafixture.Token)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
