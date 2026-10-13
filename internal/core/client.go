// SPDX-License-Identifier: 0BSD

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	url2 "net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	// ErrChallenge means the site answered with a bot check page
	// instead of content.
	ErrChallenge = errors.New("site challenge page, rate limited")
	// ErrInterrupted means the context was cancelled by the user.
	ErrInterrupted = errors.New("interrupted")
)

// Client wraps http.Client with politeness pacing, retry/backoff and
// an optional FlareSolverr fallback for challenge pages.
type Client struct {
	hc        *http.Client
	hcNoRedir *http.Client
	ua        string
	delay     time.Duration
	retries   int
	term      *Term

	fsURL string // FlareSolverr /v1 endpoint, empty disables

	mu      sync.Mutex
	lastReq time.Time
	uaMu    sync.Mutex
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

// SetFlareSolverr configures the FlareSolverr endpoint used when a
// page returns a bot challenge. Expects the base URL like
// http://localhost:8191 (path appended automatically).
func (c *Client) SetFlareSolverr(u string) {
	c.fsURL = strings.TrimRight(u, "/")
}

// SetUA swaps the user agent. FlareSolverr cookie clearance is bound
// to the UA that solved the challenge, so we adopt it after a solve.
func (c *Client) SetUA(ua string) {
	c.uaMu.Lock()
	c.ua = ua
	c.uaMu.Unlock()
}

func (c *Client) UA() string {
	c.uaMu.Lock()
	defer c.uaMu.Unlock()
	return c.ua
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
func (c *Client) do(ctx context.Context, noRedir bool, method, urlStr, referer string, extra http.Header) (*http.Response, error) {
	var lastErr error
	challengeWaits := 0
	for attempt := 0; attempt <= c.retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, ErrInterrupted
		}
		if err := c.pace(ctx); err != nil {
			return nil, ErrInterrupted
		}
		req, err := http.NewRequestWithContext(ctx, method, urlStr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", c.UA())
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
			c.term.Debugf("request error: %s: %v", urlStr, err)
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
			return nil, fmt.Errorf("not found (404): %s", urlStr)

		case resp.StatusCode == http.StatusTooManyRequests:
			resp.Body.Close()
			wait := retryAfter(resp)
			if wait <= 0 {
				wait = backoff(attempt, 5*time.Second)
			}
			c.term.Warnf("rate limited (429) on %s, waiting %s", ShortURL(urlStr), wait.Round(time.Second))
			if !sleepCtx(ctx, wait) {
				return nil, ErrInterrupted
			}
			lastErr = fmt.Errorf("429 too many requests: %s", urlStr)

		case isChallenge(resp.StatusCode, body):
			resp.Body.Close()
			challengeWaits++
			if c.fsURL != "" {
				// FlareSolverr will handle it; fail fast so callers
				// can route through it immediately.
				return nil, fmt.Errorf("%w: %s", ErrChallenge, urlStr)
			}
			if challengeWaits > 3 {
				return nil, fmt.Errorf("%w: %s", ErrChallenge, urlStr)
			}
			wait := time.Duration(challengeWaits) * 20 * time.Second
			c.term.Warnf("challenge page on %s, backing off %s", ShortURL(urlStr), wait)
			if !sleepCtx(ctx, wait) {
				return nil, ErrInterrupted
			}
			lastErr = ErrChallenge

		case resp.StatusCode >= 500:
			resp.Body.Close()
			lastErr = fmt.Errorf("server error %d: %s", resp.StatusCode, urlStr)
			if !sleepCtx(ctx, backoff(attempt, 0)) {
				return nil, ErrInterrupted
			}

		default:
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, urlStr)
		}
	}
	return nil, lastErr
}

// Get fetches a page and returns the body, falling back to
// FlareSolverr when a challenge page is detected.
func (c *Client) Get(ctx context.Context, url, referer string) ([]byte, error) {
	resp, err := c.do(ctx, false, http.MethodGet, url, referer, nil)
	if err != nil {
		if errors.Is(err, ErrChallenge) && c.fsURL != "" {
			c.term.Infof("solving challenge for %s via flaresolverr", ShortURL(url))
			body, ferr := c.flareGet(ctx, url)
			if ferr == nil {
				return body, nil
			}
			c.term.Warnf("flaresolverr failed for %s: %v", ShortURL(url), ferr)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// GetJSON fetches a JSON API endpoint with extra headers and decodes
// into v.
func (c *Client) GetJSON(ctx context.Context, url, referer string, extra http.Header, v any) error {
	resp, err := c.do(ctx, false, http.MethodGet, url, referer, extra)
	if err != nil {
		if errors.Is(err, ErrChallenge) && c.fsURL != "" {
			body, ferr := c.flareGet(ctx, url)
			if ferr != nil {
				return err
			}
			return json.Unmarshal(body, v)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, url)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

// PostJSON sends a JSON or form POST and decodes the response.
func (c *Client) PostJSON(ctx context.Context, url string, form url2.Values, jsonBody any, extra http.Header, v any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else if jsonBody != nil {
		b, err := json.Marshal(jsonBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	if err := c.pace(ctx); err != nil {
		return ErrInterrupted
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", c.UA())
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range extra {
		for _, s := range vs {
			req.Header.Set(k, s)
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("status %d: %s", resp.StatusCode, url)
	}
	if v == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
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
		base, _ := url2.Parse(url)
		loc = base.Scheme + "://" + base.Host + loc
	}
	return loc, nil
}

// Open starts a download. extra carries headers such as Range.
func (c *Client) Open(ctx context.Context, url, referer string, extra http.Header) (*http.Response, error) {
	return c.do(ctx, false, http.MethodGet, url, referer, extra)
}

// Headers returns the last observed rate limit headers for sources
// that expose them (nexus X-RL-*, modrinth X-Ratelimit-*). Currently
// informational only.
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

// ShortURL trims a URL for log output.
func ShortURL(u string) string {
	const max = 80
	if len(u) > max {
		return u[:max-3] + "..."
	}
	return u
}

// flaresolverr protocol types
type fsRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type fsCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}

type fsResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL       string     `json:"url"`
		Status    int        `json:"status"`
		Response  string     `json:"response"`
		UserAgent string     `json:"userAgent"`
		Cookies   []fsCookie `json:"cookies"`
	} `json:"solution"`
}

// flareGet routes a GET through the local FlareSolverr instance and
// returns the solved page body. On success it also adopts the
// clearance cookies and user agent so later direct requests may pass.
func (c *Client) flareGet(ctx context.Context, target string) ([]byte, error) {
	payload, _ := json.Marshal(fsRequest{Cmd: "request.get", URL: target, MaxTimeout: 60000})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.fsURL+"/v1", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	var fr fsResponse
	if err := json.Unmarshal(body, &fr); err != nil {
		return nil, fmt.Errorf("flaresolverr: bad response: %w", err)
	}
	if fr.Status != "ok" || fr.Solution.Status != 200 {
		return nil, fmt.Errorf("flaresolverr: %s (http %d)", fr.Message, fr.Solution.Status)
	}

	// adopt clearance cookies + UA so follow-up requests pass
	if len(fr.Solution.Cookies) > 0 {
		domains := map[string][]*http.Cookie{}
		for _, fc := range fr.Solution.Cookies {
			d := fc.Domain
			if d == "" {
				if u, err := url2.Parse(target); err == nil {
					d = u.Host
				}
			}
			domains[d] = append(domains[d], &http.Cookie{
				Name:   fc.Name,
				Value:  fc.Value,
				Domain: d,
				Path:   firstNonEmptyS(fc.Path, "/"),
			})
		}
		for d, cs := range domains {
			u := &url2.URL{Scheme: "https", Host: strings.TrimPrefix(d, ".")}
			c.hc.Jar.SetCookies(u, cs)
			c.term.Debugf("flaresolverr: adopted %d cookie(s) for %s", len(cs), d)
		}
		if fr.Solution.UserAgent != "" && fr.Solution.UserAgent != c.UA() {
			c.SetUA(fr.Solution.UserAgent)
			c.term.Debugf("flaresolverr: adopted UA %s", fr.Solution.UserAgent)
		}
	}
	return []byte(fr.Solution.Response), nil
}

func firstNonEmptyS(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
