package main

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadinessAndProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	defer listener.Close()
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	for _, ready := range []bool{true, false} {
		w := httptest.NewRecorder()
		handler(ready).ServeHTTP(w, httptest.NewRequest("GET", "/ready", nil))
		if (w.Code == http.StatusOK) != ready {
			t.Fatalf("ready=%t status=%d", ready, w.Code)
		}
	}
	if !probe(context.Background(), address, logger) || !strings.Contains(logs.String(), "connection succeeded") {
		t.Fatal("probe did not connect")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if probe(context.Background(), address, logger) || !strings.Contains(logs.String(), "connection failed") {
		t.Fatal("failed probe hidden")
	}
	if probe(context.Background(), "", logger) {
		t.Fatal("missing upstream accepted")
	}

}

type logChannel chan string

func (c logChannel) Write(p []byte) (int, error) { c <- string(p); return len(p), nil }

func TestPeriodicProbesStopOnCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lines := make(logChannel, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		probeLoop(ctx, listener.Addr().String(), 5*time.Millisecond, log.New(lines, "", 0))
	}()
	for i := 0; i < 2; i++ {
		select {
		case line := <-lines:
			if !strings.Contains(line, "connection succeeded") {
				t.Fatal("periodic probe failed")
			}
		case <-time.After(time.Second):
			t.Fatal("no periodic TCP probe")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe loop ignored cancellation")
	}
}
