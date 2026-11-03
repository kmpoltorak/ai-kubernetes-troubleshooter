// Command fixture supplies bounded HTTP readiness and TCP probes for live tests.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func handler(ready bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !ready {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	return mux
}

func probe(ctx context.Context, upstream string, logger *log.Logger) bool {
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", upstream)
	if err != nil {
		logger.Printf("connection failed: %v", err)
		return false
	}
	if err := conn.Close(); err != nil {
		logger.Printf("connection cleanup failed: %v", err)
	}
	logger.Print("connection succeeded")
	return true
}

func probeLoop(ctx context.Context, upstream string, interval time.Duration, logger *log.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probe(ctx, upstream, logger)
		}
	}
}

func main() {
	ready := flag.Bool("ready", true, "readiness response")
	upstream := flag.String("upstream", "", "fixed destination for periodic TCP probes")
	flag.Parse()
	logger := log.New(os.Stdout, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *upstream != "" {
		go probeLoop(ctx, *upstream, 2*time.Second, logger)
	}
	srv := &http.Server{Addr: ":8080", Handler: handler(*ready), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(deadline); err != nil {
			logger.Printf("shutdown: %v", err)
		}
	}()
	logger.Print("fixture started")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}
