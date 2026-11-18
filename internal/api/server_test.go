package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

func newServer(db Pinger) *Server {
	return &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: db}
}

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body is not JSON: %q", path, rec.Body.String())
	}
	return rec, body
}

func TestHealthzIgnoresDatabase(t *testing.T) {
	h := newServer(pinger{err: errors.New("down")}).Handler()
	rec, body := get(t, h, "/healthz")
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("got %d %v", rec.Code, body)
	}
	if _, ok := body["version"]; !ok {
		t.Error("healthz should report the version")
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name string
		db   Pinger
		want int
	}{
		{"up", pinger{}, http.StatusOK},
		{"down", pinger{err: errors.New("down")}, http.StatusServiceUnavailable},
		{"none", nil, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, _ := get(t, newServer(tt.db).Handler(), "/readyz")
			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestUnknownPathIsJSON404(t *testing.T) {
	rec, body := get(t, newServer(nil).Handler(), "/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if e, _ := body["error"].(map[string]any); e["code"] != "not_found" {
		t.Errorf("body = %v", body)
	}
}

func TestListenAndServeShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- ListenAndServe(ctx, log, addr, newServer(nil).Handler()) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}
