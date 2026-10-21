// Command agent is the rackanvil node agent entrypoint.
//
// It enrolls with the server (initialize), heartbeats (checkin), executes
// dispatched shell tasks and reports results.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Oumu33/rackanvil/internal/agent"
	"github.com/Oumu33/rackanvil/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	serverURL := flag.String("server", "http://127.0.0.1:9100", "management server base URL")
	interval := flag.Duration("interval", 30*time.Second, "checkin interval")
	metricsInterval := flag.Duration("metrics-interval", 60*time.Second, "metrics upload interval (0 disables)")
	flag.Parse()
	if *showVersion {
		fmt.Println("rackanvil-agent " + version.String())
		return
	}

	logger := log.New(os.Stdout, "agent: ", log.LstdFlags)
	logger.Printf("%s", version.String())
	a := agent.New(*serverURL, *interval, logger).WithMetricsInterval(*metricsInterval)

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		logger.Printf("shutting down")
		cancel()
	}()

	if err := a.Run(ctx); err != nil {
		logger.Fatalf("agent: %v", err)
	}
}
