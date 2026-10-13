// SPDX-License-Identifier: 0BSD

package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// Server ignores Range entirely and answers 200 with the full body.
// The downloader must detect this and restart instead of appending.
func TestDownloadRangeIgnored(t *testing.T) {
	payload := []byte("0123456789abcdef")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload) // ignores Range on purpose
	}))
	defer srv.Close()

	dir := t.TempDir()
	part := filepath.Join(dir, "f.bin.part")
	os.WriteFile(part, payload[:8], 0o644)

	task := &Task{
		Title: "t", DirectURL: srv.URL + "/f",
		DestDir: dir, Filename: "f.bin", Size: 16,
	}
	res, err := FetchTask(context.Background(), testClient(t), task, "fast", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(res.Path)
	if len(got) != 16 || string(got) != string(payload) {
		t.Fatalf("got %d bytes %q", len(got), got)
	}
}

// Part already complete -> 416 means the file is done.
func TestDownload416Done(t *testing.T) {
	payload := []byte("complete-file")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	part := filepath.Join(dir, "f.bin.part")
	os.WriteFile(part, payload, 0o644)

	task := &Task{
		Title: "t", DirectURL: srv.URL + "/f",
		DestDir: dir, Filename: "f.bin", Size: int64(len(payload)),
	}
	res, err := FetchTask(context.Background(), testClient(t), task, "fast", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(res.Path)
	if string(got) != string(payload) {
		t.Fatal("content changed on 416 finalize")
	}
}

// Mid-stream EOF: the connection drops halfway. Must retry and
// resume rather than accept a truncated file.
func TestDownloadMidStreamDrop(t *testing.T) {
	payload := []byte("1234567890abcdefghijklmnopqrstuvwxyz")
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// first attempt dies mid stream
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			w.Write(payload[:10])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return // body ends early
		}
		lo := 0
		if rng := r.Header.Get("Range"); rng != "" {
			fmt.Sscanf(rng, "bytes=%d-", &lo)
		}
		if lo > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[lo:])
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	task := &Task{
		Title: "t", DirectURL: srv.URL + "/f",
		DestDir: dir, Filename: "f.bin", Size: int64(len(payload)),
	}
	res, err := FetchTask(context.Background(), testClient(t), task, "fast", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(res.Path)
	if string(got) != string(payload) {
		t.Fatalf("got %q", got)
	}
	if atomic.LoadInt32(&calls) < 2 {
		t.Fatal("expected a retry after mid-stream drop")
	}
}

// 429 with Retry-After must be honored and retried.
func TestClientRetry429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("t", 0, 5, 10*time.Second, term)

	body, err := c.Get(context.Background(), srv.URL+"/p", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("calls = %d", calls)
	}
}

// Flaky 5xx eventually succeeds.
func TestClientRetry5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte("fine"))
	}))
	defer srv.Close()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("t", 0, 5, 10*time.Second, term)
	body, err := c.Get(context.Background(), srv.URL+"/p", "")
	if err != nil || string(body) != "fine" {
		t.Fatalf("got %q err %v", body, err)
	}
}

// 404 must come back as a typed HTTPError, not a retry.
func TestClient404Typed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("t", 0, 5, 10*time.Second, term)
	_, err := c.Get(context.Background(), srv.URL+"/missing", "")
	var he *HTTPError
	if !errors.As(err, &he) || he.Code != 404 {
		t.Fatalf("err = %v, want HTTPError 404", err)
	}
}

// 401 surfaces as auth failure via IsAuth.
func TestClient401IsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("t", 0, 0, 10*time.Second, term)
	_, err := c.Get(context.Background(), srv.URL+"/x", "")
	if !IsAuth(err) {
		t.Fatalf("err = %v, want auth error", err)
	}
}

// Garbage JSON must fail the decode, not produce empty structs.
func TestGetJSONGarbage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not json"))
	}))
	defer srv.Close()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("t", 0, 0, 10*time.Second, term)
	var out struct{ X int }
	if err := c.GetJSON(context.Background(), srv.URL, "", nil, &out); err == nil {
		t.Fatal("expected decode error")
	}
}

// Resolve follows relative Location headers onto the same host.
func TestResolveRelativeLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/final/path")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("t", 0, 0, 10*time.Second, term)
	loc, err := c.Resolve(context.Background(), srv.URL+"/redir", "")
	if err != nil {
		t.Fatal(err)
	}
	if loc != srv.URL+"/final/path" {
		t.Fatalf("loc = %q", loc)
	}
}
