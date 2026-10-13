// SPDX-License-Identifier: 0BSD

package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	kindFile  = "file"
	kindMedia = "media"
)

type dlTask struct {
	id       string
	title    string
	kind     string
	destDir  string
	filename string
	pageURL  string // stable page URL, used as referer
	startURL string // /downloads/start/<id>, kind file only
	mediaURL string // direct media URL, kind media only
	size     int64
	md5      string
}

type dlResult struct {
	task     *dlTask
	path     string
	size     int64
	fastHash string
	md5OK    bool
	skipped  bool
	err      error
}

type progressFn func(done, total int64)

// downloadOne fetches a single task to disk with resume support.
// Files land in destDir under dest.ext, data first accumulates in
// dest.ext.part so an interrupted run can continue later.
func downloadOne(ctx context.Context, c *Client, t *dlTask, hashMode string, prog progressFn) (*dlResult, error) {
	res := &dlResult{task: t}

	url := t.mediaURL
	if t.kind == kindFile {
		cdn, err := resolveFileURL(ctx, c, t)
		if err != nil {
			return res, err
		}
		url = cdn
	}

	name := t.filename
	if name == "" {
		name = nameFromURL(url)
	}
	name = SanitizeName(name)
	dest := filepath.Join(t.destDir, name)
	part := dest + ".part"

	// Skip files already on disk. When the site gave us an MD5 the
	// copy is verified first so a corrupt file is re-fetched rather
	// than silently kept.
	if st, err := os.Stat(dest); err == nil && st.Size() > 0 {
		if t.size == 0 || st.Size() == t.size {
			skip := t.md5 == ""
			if !skip {
				if sum, herr := MD5File(dest); herr == nil && strings.EqualFold(sum, t.md5) {
					skip = true
					res.md5OK = true
				}
			}
			if skip {
				res.path, res.size, res.skipped = dest, st.Size(), true
				return res, nil
			}
			os.Remove(dest)
		}
	}

	var offset int64
	if st, err := os.Stat(part); err == nil {
		offset = st.Size()
	}

	const attempts = 3
	var lastErr error
	for try := 0; try < attempts; try++ {
		if ctx.Err() != nil {
			return res, ErrInterrupted
		}
		if try > 0 && t.kind == kindFile {
			// The signed CDN URL may have expired; mint a new one.
			if cdn, err := resolveFileURL(ctx, c, t); err == nil {
				url = cdn
			}
		}
		offset, lastErr = streamTo(ctx, c, url, t.pageURL, part, offset, prog)
		if lastErr == nil {
			break
		}
		if lastErr == ErrInterrupted {
			return res, ErrInterrupted
		}
		// A partial file larger than the expected size is stale,
		// restart it instead of failing on the size check.
		if t.size > 0 {
			if st, serr := os.Stat(part); serr == nil && st.Size() > t.size {
				os.Remove(part)
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
	if t.size > 0 && st.Size() != t.size {
		return res, fmt.Errorf("size mismatch: got %d bytes, expected %d", st.Size(), t.size)
	}
	if err := os.Rename(part, dest); err != nil {
		return res, err
	}
	res.path, res.size = dest, st.Size()

	if hashMode != "none" {
		if fh, _, err := FastHash(dest); err == nil {
			res.fastHash = fh
		}
		if t.md5 != "" {
			sum, err := MD5File(dest)
			if err != nil {
				return res, err
			}
			res.md5OK = strings.EqualFold(sum, t.md5)
			if !res.md5OK {
				os.Remove(dest)
				return res, fmt.Errorf("md5 mismatch: got %s, expected %s", sum, t.md5)
			}
		}
	}
	return res, nil
}

// resolveFileURL walks start page -> mirror page -> signed CDN URL.
func resolveFileURL(ctx context.Context, c *Client, t *dlTask) (string, error) {
	body, err := c.Get(ctx, t.startURL, t.pageURL)
	if err != nil {
		return "", fmt.Errorf("download start page: %w", err)
	}
	mirror := ParseStartPage(body)
	if mirror == "" {
		return "", fmt.Errorf("no mirror link on %s", t.startURL)
	}
	cdn, err := c.Resolve(ctx, mirror, t.startURL)
	if err != nil {
		return "", fmt.Errorf("mirror: %w", err)
	}
	return cdn, nil
}

// streamTo appends the body of url to part starting at offset.
// Returns the new offset so callers can resume after errors.
func streamTo(ctx context.Context, c *Client, url, referer, part string, offset int64, prog progressFn) (int64, error) {
	var extra http.Header
	if offset > 0 {
		extra = http.Header{"Range": []string{fmt.Sprintf("bytes=%d-", offset)}}
	}
	resp, err := c.Open(ctx, url, referer, extra)
	if err != nil {
		return offset, err
	}
	defer resp.Body.Close()

	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		// server honored the range
	case offset > 0 && resp.StatusCode == http.StatusOK:
		// range ignored, restart the file
		offset = 0
	case offset > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// whole file already on disk
		return offset, nil
	case resp.StatusCode != http.StatusOK:
		return offset, fmt.Errorf("status %d downloading %s", resp.StatusCode, shortURL(url))
	}

	flag := os.O_WRONLY | os.O_CREATE
	if offset > 0 {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flag, 0o644)
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
			f.Sync()
			f.Close()
			return offset, ErrInterrupted
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
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
			f.Sync()
			f.Close()
			return offset, rerr
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return offset, err
	}
	return offset, f.Close()
}

func nameFromURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i]
	}
	if i := strings.LastIndexByte(u, '/'); i >= 0 && i+1 < len(u) {
		return u[i+1:]
	}
	return "download.bin"
}
