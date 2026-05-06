package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerUsesConfiguredTotalTimeout(t *testing.T) {
	totalTimeout := 1800 * time.Second
	srv := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), totalTimeout)

	if srv.ReadTimeout != totalTimeout {
		t.Fatalf("ReadTimeout=%s, want %s", srv.ReadTimeout, totalTimeout)
	}
	if srv.WriteTimeout != totalTimeout {
		t.Fatalf("WriteTimeout=%s, want %s", srv.WriteTimeout, totalTimeout)
	}
	if srv.ReadHeaderTimeout != 15*time.Second {
		t.Fatalf("ReadHeaderTimeout=%s, want 15s", srv.ReadHeaderTimeout)
	}
}
