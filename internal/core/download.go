// SPDX-License-Identifier: 0BSD

package core

import (
	"context"
	"crypto/md5"  // #nosec G501 -- provider digests
	"crypto/sha1" // #nosec G505 -- provider digests
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ProgressFn receives bytes done and total (-1 when unknown).
type ProgressFn func(done, total int64)

// Task is one unit of download work queued for the pool. Exactly one
// fetch strategy applies:
//
//   - Resolve != nil: called to mint a fresh download URL (signed CDN
//     links, mirror pages), then streamed with Range resume.
//   - DirectURL != "": streamed directly with Range resume.
//   - Run != nil: custom fetcher (e.g. steamcmd), no HTTP involved.
type Task struct {
	ID       string
	Section  string
	Title    string
	DestDir  string
	Filename string
	PageURL  string
	Size     int64
	Hashes   map[string]string // remote digests to verify: md5, sha1, sha256, sha512

	DirectURL string
	Resolve   func(ctx context.Context) (string, error)
	Run       func(ctx context.Context, dest string, prog ProgressFn) (*Result, error)
	Header    http.Header // extra request headers for the download

	// Entry points back at the manifest row, updated by the pool.
	Entry *ItemEntry
}

// Result reports what landed on disk.
type Result struct {
	Path     string
	Size     int64
	FastHash string
	SHA256   string
	HashOK   bool
	Skipped  bool
}

func newHasher(name string) hash.Hash {
	switch name {
	case "md5":
		return md5.New() // #nosec G401
	case "sha1":
		return sha1.New() // #nosec G401
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	}
	return nil
}

// verifyHashes computes the digest for each declared algorithm and
// compares. Missing algorithm support is ignored, a present but
// different digest is an error.
func verifyHashes(path string, expect map[string]string) (bool, error) {
	for algo, want := range expect {
		h := newHasher(algo)
		if h == nil || want == "" {
			continue
		}
		sum, err := SumFile(path, h)
		if err != nil {
			return false, err
		}
		if !strings.EqualFold(sum, want) {
			return false, fmt.Errorf("%s mismatch: got %s, want %s", algo, sum, want)
		}
	}
	return len(expect) > 0, nil
}

// FetchTask runs a single task to disk with resume support. Data lands
// in <dest>.part first so an interrupted run can continue later.
func FetchTask(ctx context.Context, c *Client, t *Task, hashMode string, prog ProgressFn) (*Result, error) {
	if err := os.MkdirAll(t.DestDir, 0o750); err != nil {
		return nil, err
	}
	if t.Run != nil {
		return t.Run(ctx, filepath.Join(t.DestDir, t.Filename), prog)
	}

	res := &Result{}
	url := t.DirectURL
	if t.Resolve != nil {
		u, err := t.Resolve(ctx)
		if err != nil {
			return res, err
		}
		url = u
	}
	if url == "" {
		return res, fmt.Errorf("no download URL for %s", t.Title)
	}

	name := t.Filename
	if name == "" {
		name = NameFromURL(url)
	}
	name = SanitizeName(name)
	dest := filepath.Join(t.DestDir, name)
	part := dest + ".part"

	// Skip files already on disk. When the remote side published a
	// digest the copy is verified first so a corrupt file is re-fetched
	// rather than silently kept.
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		if t.Size == 0 || st.Size() == t.Size {
			skip := len(t.Hashes) == 0
			if !skip {
				if ok, herr := verifyHashes(dest, t.Hashes); herr == nil && ok {
					skip = true
					res.HashOK = true
				}
			}
			if skip {
				res.Path, res.Size, res.Skipped = dest, st.Size(), true
				return res, nil
			}
			_ = os.Remove(dest)
		}
	}

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	const attempts = 3
	var lastErr error
	for try := range attempts {
		if ctx.Err() != nil {
			return res, ErrInterrupted
		}
		if try > 0 && t.Resolve != nil {
			// The signed URL may have expired. Mint a new one.
			if u, err := t.Resolve(ctx); err == nil {
				url = u
			}
		}
		offset, lastErr = streamTo(ctx, c, url, t.PageURL, part, offset, t.Header, prog)
		if lastErr == nil {
			break
		}
		if lastErr == ErrInterrupted {
			return res, ErrInterrupted
		}
		// A partial file larger than the expected size is stale,
		// restart it instead of failing on the size check.
		if t.Size > 0 {
			if st, serr := os.Stat(part); serr == nil && st.Size() > t.Size {
				_ = os.Remove(part)
				offset = 0
			}
		}
	}
	if lastErr != nil {
		return res, lastErr
	}

	st, err := os.Stat(part)
	if err != nil {
		return res, err
	}
	if t.Size > 0 && st.Size() != t.Size {
		return res, fmt.Errorf("size mismatch: got %d bytes, expected %d", st.Size(), t.Size)
	}
	if err := os.Rename(part, dest); err != nil {
		return res, err
	}
	res.Path, res.Size = dest, st.Size()

	if hashMode != "none" {
		if fh, _, err := FastHash(dest); err == nil {
			res.FastHash = fh
		}
		if ok, err := verifyHashes(dest, t.Hashes); err != nil {
			_ = os.Remove(dest)
			return res, err
		} else {
			res.HashOK = ok
		}
		if hashMode == "full" {
			if s, err := SHA256File(dest); err == nil {
				res.SHA256 = s
			}
		}
	}
	return res, nil
}

// streamTo appends the body of url to part starting at offset.
// Returns the new offset so callers can resume after errors.
func streamTo(ctx context.Context, c *Client, url, referer, part string, offset int64, hdr http.Header, prog ProgressFn) (int64, error) {
	extra := http.Header{}
	maps.Copy(extra, hdr)
	if offset > 0 {
		extra.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.Open(ctx, url, referer, extra)
	if err != nil {
		var he *HTTPError
		if offset > 0 && errors.As(err, &he) && he.Code == http.StatusRequestedRangeNotSatisfiable {
			// whole file already on disk
			return offset, nil
		}
		return offset, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		// server honored the range
	case offset > 0 && resp.StatusCode == http.StatusOK:
		// range ignored, restart the file
		offset = 0
	case resp.StatusCode != http.StatusOK:
		return offset, fmt.Errorf("status %d downloading %s", resp.StatusCode, ShortURL(url))
	}

	flag := os.O_WRONLY | os.O_CREATE
	if offset > 0 {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	// #nosec G304 -- part path derives from the sanitized filename
	f, err := os.OpenFile(part, flag, 0o600)
	if err != nil {
		return offset, err
	}

	var total int64 = -1
	if resp.ContentLength > 0 {
		total = resp.ContentLength + offset
	}
	if prog != nil {
		prog(offset, total)
	}

	buf := make([]byte, 64<<10)
	for {
		if ctx.Err() != nil {
			_ = f.Sync()
			_ = f.Close()
			return offset, ErrInterrupted
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				_ = f.Close()
				return offset, werr
			}
			offset += int64(n)
			if prog != nil {
				prog(offset, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			_ = f.Sync()
			_ = f.Close()
			return offset, rerr
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return offset, err
	}
	return offset, f.Close()
}

func NameFromURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndexByte(u, '/'); i >= 0 && i+1 < len(u) {
		return u[i+1:]
	}
	return "download.bin"
}
