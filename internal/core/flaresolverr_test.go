// SPDX-License-Identifier: 0BSD

package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// challengeSrv always answers with a Cloudflare-style challenge.
func challengeSrv(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<html><title>Just a moment...</title>window._cf_chl_opt = {};</html>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fsSrv fakes FlareSolverr solving the challenge.
func fsSrv(t *testing.T, body string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req fsRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Cmd != "request.get" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"solution": map[string]any{
				"url":       req.URL,
				"status":    200,
				"response":  body,
				"userAgent": "solved-ua",
				"cookies": []map[string]string{
					{"name": "cf_clearance", "value": "abc", "domain": "example.com", "path": "/"},
				},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFlareSolverrFallback(t *testing.T) {
	origin := challengeSrv(t)
	solved := fsSrv(t, "<html>solved page body</html>")

	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("test", 0, 1, 10*time.Second, term)
	c.SetFlareSolverr(solved.URL)

	body, err := c.Get(context.Background(), origin.URL+"/page", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<html>solved page body</html>" {
		t.Fatalf("got %q", body)
	}
	if c.UA() != "solved-ua" {
		t.Fatalf("ua not adopted: %q", c.UA())
	}
}

func TestChallengeWithoutSolverr(t *testing.T) {
	origin := challengeSrv(t)
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	term := NewTerm(devnull, "never", true, false)
	c := NewClient("test", 0, 5, 10*time.Second, term)

	// without flaresolverr a challenge triggers 20s+ backoff waits;
	// cancel mid-wait to prove the interrupt path works.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := c.Get(ctx, origin.URL+"/page", "")
	if err != ErrInterrupted {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
}
