// Package doppler is a thin client over the Doppler v3 REST API.
//
// It talks to the API directly rather than shelling out to the `doppler` CLI
// for one reason: conditional requests. Every endpoint dpx reads returns a
// weak ETag and honors If-None-Match with a 304, which is what makes a
// revalidation nearly free — the CLI gives no way to reach that.
package doppler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client is safe for concurrent use.
type Client struct {
	http    *http.Client
	apiHost string
	token   string

	// rate mirrors the last seen x-ratelimit-* headers so the UI can show
	// headroom without a probe request.
	rate RateState
}

// RateState is the last observed rate-limit window.
type RateState struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

// New builds a client. apiHost may be empty for the public API.
func New(apiHost, token string) *Client {
	if apiHost == "" {
		apiHost = "https://api.doppler.com"
	}
	return &Client{
		http:    &http.Client{Timeout: 30 * time.Second},
		apiHost: strings.TrimRight(apiHost, "/"),
		token:   token,
	}
}

// Rate returns the last observed rate-limit state.
func (c *Client) Rate() RateState { return c.rate }

// Result carries one response, or the fact that the caller's ETag is still good.
type Result struct {
	// NotModified is true when the server answered 304 and Body is empty.
	NotModified bool
	ETag        string
	Body        []byte
}

// APIError is a non-2xx response with Doppler's message extracted.
type APIError struct {
	Status  int
	Message string
	Path    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("doppler %s: HTTP %d", e.Path, e.Status)
	}
	return fmt.Sprintf("doppler %s: %s (HTTP %d)", e.Path, e.Message, e.Status)
}

// Unauthorized reports whether the token was rejected, which the caller turns
// into "run doppler login" rather than a generic failure.
func (e *APIError) Unauthorized() bool { return e.Status == 401 || e.Status == 403 }

// get issues a conditional GET. A non-empty etag turns it into a
// revalidation: on 304 the caller keeps whatever it already had.
func (c *Client) get(ctx context.Context, path string, q url.Values, etag string) (Result, error) {
	u := c.apiHost + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Result{}, err
	}
	// Doppler authenticates a token as HTTP basic auth with an empty password.
	req.SetBasicAuth(c.token, "")
	req.Header.Set("Accept", "application/json")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	defer resp.Body.Close()
	c.observeRate(resp)

	if resp.StatusCode == http.StatusNotModified {
		// The server does not repeat the ETag on a 304, so hand back the one
		// we sent — the entry it validates stays addressable by it.
		return Result{NotModified: true, ETag: etag}, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, &APIError{Status: resp.StatusCode, Message: apiMessage(body), Path: path}
	}
	return Result{ETag: resp.Header.Get("ETag"), Body: body}, nil
}

func (c *Client) observeRate(resp *http.Response) {
	h := resp.Header
	if v, err := strconv.Atoi(h.Get("x-ratelimit-limit")); err == nil {
		c.rate.Limit = v
	}
	if v, err := strconv.Atoi(h.Get("x-ratelimit-remaining")); err == nil {
		c.rate.Remaining = v
	}
	if v, err := strconv.ParseInt(h.Get("x-ratelimit-reset"), 10, 64); err == nil {
		c.rate.ResetAt = time.Unix(v, 0)
	}
}

// apiMessage pulls the human-readable error out of Doppler's error envelope,
// which puts it under either "message" or a "messages" array.
func apiMessage(body []byte) string {
	var env struct {
		Message  string   `json:"message"`
		Messages []string `json:"messages"`
		Error    string   `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return strings.TrimSpace(string(body))
	}
	switch {
	case env.Message != "":
		return env.Message
	case len(env.Messages) > 0:
		return strings.Join(env.Messages, "; ")
	case env.Error != "":
		return env.Error
	}
	return strings.TrimSpace(string(body))
}
