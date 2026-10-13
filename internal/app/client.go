// SPDX-License-Identifier: 0BSD

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrChallenge means the site answered with a bot check page
	// instead of content. Retrying later with a slower delay is the
	// only remedy short of a browser.
	ErrChallenge = errors.New("site challenge page, rate limited")
	// ErrInterrupted means the context was cancelled by the user.
	ErrInterrupted = errors.New("interrupted")
)

type Client struct {
	hc        *http.Client
	hcNoRedir *http.Client
	ua        string
	delay     time.Duration
	retries   int
	term      *Term

	mu      sync.Mutex
	lastReq time.Time
}

func NewClient(ua string, delay time.Duration, retries int, timeout time.Duration, term *Term) *Client {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		hc: &http.Client{Jar: jar, Transport: tr, Timeout: timeout},
		hcNoRedir: &http.Client{
			Jar:       jar,
			Transport: tr,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		ua:      ua,
		delay:   delay,
		retries: retries,
		term:    term,
	}
}

// pace enforces a minimum gap between request starts so polite
// scraping does not trip the rate limiter.
func (c *Client) pace(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.delay - time.Since(c.lastReq); w > 0 {
		t := time.NewTimer(w)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	c.lastReq = time.Now()
	return nil
}

func isChallenge(status int, body []byte) bool {
	if status != http.StatusForbidden && status != http.StatusServiceUnavailable {
		return false
	}
	if len(body) == 0 {
		return false
	}
	s := string(body[:min(len(body), 65536)])
	return strings.Contains(s, "Just a moment") ||
		strings.Contains(s, "_cf_chl_opt") ||
		strings.Contains(s, "cf-mitigated")
}

// do runs one request with retry and backoff. It returns the
// response with an open body on success.
func (c *Client) do(ctx context.Context, noRedir bool, method, url, referer string, extra http.Header) (*http.Response, error) {
	var lastErr error
	challengeWaits := 0
	for attempt := 0; attempt <= c.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, ErrInterrupted
		}
		if err := c.pace(ctx); err != nil {
			return nil, ErrInterrupted
		}
		req, err := http.NewRequestWithContext(ctx, method, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.ua)
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		for k, vs := range extra {
			for _, v := range vs {
				req.Header.Set(k, v)
			}
		}

		hc := c.hc
		if noRedir {
			hc = c.hcNoRedir
		}
		resp, err := hc.Do(req)
		if err != nil {
			lastErr = err
			c.term.Debugf("request error: %s: %v", url, err)
			if !sleepCtx(ctx, backoff(attempt, 0)) {
				return nil, ErrInterrupted
			}
			continue
		}

		// Cheap body sniff for challenge pages on 403/503.
		var body []byte
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
		}

		switch {
		case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent ||
			resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusFound ||
			resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusTemporaryRedirect:
			return resp, nil

		case resp.StatusCode == http.StatusNotFound:
			resp.Body.Close()
			return nil, fmt.Errorf("not found (404): %s", url)

		case resp.StatusCode == http.StatusTooManyRequests:
			resp.Body.Close()
			wait := retryAfter(resp)
			if wait <= 0 {
				wait = backoff(attempt, 5*time.Second)
			}
			c.term.Warnf("rate limited (429) on %s, waiting %s", shortURL(url), wait.Round(time.Second))
			if !sleepCtx(ctx, wait) {
				return nil, ErrInterrupted
			}
			lastErr = fmt.Errorf("429 too many requests: %s", url)

		case isChallenge(resp.StatusCode, body):
			resp.Body.Close()
			challengeWaits++
			if challengeWaits > 3 {
				return nil, fmt.Errorf("%w: %s", ErrChallenge, url)
			}
			wait := time.Duration(challengeWaits) * 20 * time.Second
			c.term.Warnf("challenge page on %s, backing off %s", shortURL(url), wait)
			if !sleepCtx(ctx, wait) {
				return nil, ErrInterrupted
			}
			lastErr = ErrChallenge

		case resp.StatusCode >= 500:
			resp.Body.Close()
			lastErr = fmt.Errorf("server error %d: %s", resp.StatusCode, url)
			if !sleepCtx(ctx, backoff(attempt, 0)) {
				return nil, ErrInterrupted
			}

		default:
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, url)
		}
	}
	return nil, lastErr
}

// Get fetches a page and returns the body.
func (c *Client) Get(ctx context.Context, url, referer string) ([]byte, error) {
	resp, err := c.do(ctx, false, http.MethodGet, url, referer, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// Resolve performs a request without following redirects and returns
// the Location target.
func (c *Client) Resolve(ctx context.Context, url, referer string) (string, error) {
	resp, err := c.do(ctx, true, http.MethodGet, url, referer, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no redirect from %s (status %d)", url, resp.StatusCode)
	}
	if strings.HasPrefix(loc, "/") {
		loc = siteBase + loc
	}
	return loc, nil
}

// Open starts a download. extra carries headers such as Range.
func (c *Client) Open(ctx context.Context, url, referer string, extra http.Header) (*http.Response, error) {
	return c.do(ctx, false, http.MethodGet, url, referer, extra)
}

func backoff(attempt int, base time.Duration) time.Duration {
	if base == 0 {
		base = 750 * time.Millisecond
	}
	d := base << attempt
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d + time.Duration(rand.Int64N(int64(500*time.Millisecond)))
}

func retryAfter(resp *http.Response) time.Duration {
	ra := resp.Header.Get("Retry-After")
	if ra == "" {
		return 0
	}
	if s, err := strconv.Atoi(ra); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(ra); err == nil {
		return time.Until(t)
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func shortURL(u string) string {
	const max = 80
	if len(u) > max {
		return u[:max-3] + "..."
	}
	return u
}
