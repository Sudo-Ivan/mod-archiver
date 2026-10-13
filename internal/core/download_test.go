// SPDX-License-Identifier: 0BSD

package core

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testServer serves a payload honoring Range like the real CDN.
func testServer(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lo := 0
		if rng := r.Header.Get("Range"); rng != "" {
			fmt.Sscanf(rng, "bytes=%d-", &lo)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-lo))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[lo:])
			return
		}
		w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient(t *testing.T) *Client {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	term := NewTerm(devnull, "never", true, false)
	return NewClient("test", 0, 2, 10*time.Second, term)
}

func TestDownloadFresh(t *testing.T) {
	payload := []byte(strings.Repeat("mod-archive-test-payload-", 5000))
	srv := testServer(t, payload)
	sum := md5.Sum(payload)
	task := &Task{
		Title: "test", DirectURL: srv.URL + "/file.bin",
		DestDir: t.TempDir(), Filename: "file.bin",
		Size:   int64(len(payload)),
		Hashes: map[string]string{"md5": hex.EncodeToString(sum[:])},
	}
	res, err := FetchTask(context.Background(), testClient(t), task, "fast", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HashOK {
		t.Error("hash not verified")
	}
	got, _ := os.ReadFile(res.Path)
	if len(got) != len(payload) {
		t.Fatalf("size %d, want %d", len(got), len(payload))
	}
	if res.FastHash == "" {
		t.Error("no fast hash")
	}
}

func TestDownloadResume(t *testing.T) {
	payload := []byte(strings.Repeat("resume-check-", 4000))
	srv := testServer(t, payload)
	dir := t.TempDir()
	part := filepath.Join(dir, "file.bin.part")
	os.WriteFile(part, payload[:len(payload)/3], 0o644)

	task := &Task{
		Title: "test", DirectURL: srv.URL + "/file.bin",
		DestDir: dir, Filename: "file.bin",
		Size: int64(len(payload)),
	}
	res, err := FetchTask(context.Background(), testClient(t), task, "fast", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(res.Path)
	if len(got) != len(payload) {
		t.Fatalf("resumed size %d, want %d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("byte %d differs after resume", i)
		}
	}
}

func TestDownloadInterrupt(t *testing.T) {
	payload := make([]byte, 8<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		for i := 0; i < len(payload); i += 64 << 10 {
			w.Write(payload[i : i+64<<10])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(30 * time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	task := &Task{
		Title: "test", DirectURL: srv.URL + "/big.bin",
		DestDir: dir, Filename: "big.bin", Size: int64(len(payload)),
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := FetchTask(ctx, testClient(t), task, "fast", nil)
	if err != ErrInterrupted {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
	part := filepath.Join(dir, "big.bin.part")
	st, serr := os.Stat(part)
	if serr != nil {
		t.Fatal("no .part file left behind")
	}
	if st.Size() == 0 || st.Size() >= int64(len(payload)) {
		t.Fatalf("partial size %d unexpected", st.Size())
	}
	if _, serr := os.Stat(filepath.Join(dir, "big.bin")); serr == nil {
		t.Fatal("dest exists after interrupt")
	}
}

func TestDownloadHashMismatch(t *testing.T) {
	srv := testServer(t, []byte("bad contents"))
	task := &Task{
		Title: "test", DirectURL: srv.URL + "/f.bin",
		DestDir: t.TempDir(), Filename: "f.bin",
		Hashes: map[string]string{"md5": "00000000000000000000000000000000"},
	}
	_, err := FetchTask(context.Background(), testClient(t), task, "fast", nil)
	if err == nil || !strings.Contains(err.Error(), "md5") {
		t.Fatalf("err = %v, want md5 mismatch", err)
	}
}
