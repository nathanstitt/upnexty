// Package quote fetches a short inspirational line for the end-of-day panel.
//
// The source is zenquotes.io, the same one the pi-dashboard reference uses.
// That dashboard proxies it through its own backend to dodge browser CORS; here
// the fetch is server-side already, so it calls the API directly.
package quote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const apiURL = "https://zenquotes.io/api/random"

// Quote is one line and its attribution.
type Quote struct {
	Text   string
	Author string
}

// Empty reports whether nothing has been fetched.
func (q Quote) Empty() bool { return q.Text == "" }

// Client fetches quotes on demand and remembers the last good one.
//
// Fetching is deliberately not on a timer. The quote is only ever shown once
// the day's events are done, which on a normal day is a few hours out of
// twenty-four -- an hourly refresh would spend most of its requests on a state
// nobody is looking at. Get fetches when asked and returns the cached line when
// the network is unavailable, so the panel renders the same either way.
type Client struct {
	// HTTP is the client used for the request. A nil value uses a default with
	// a short timeout: this runs inside the render path, and a hung request
	// would stall a frame.
	HTTP *http.Client

	mu   sync.Mutex
	last Quote
}

// Get returns a quote, fetching a fresh one and falling back to the last good
// value if that fails.
//
// It never returns an error: a missing quote is not a failure worth surfacing
// on a wall panel, and the caller renders the block without it. The error is
// returned only so a caller that wants to log it can.
func (c *Client) Get(ctx context.Context) (Quote, error) {
	q, err := c.fetch(ctx)
	if err == nil && !q.Empty() {
		c.mu.Lock()
		c.last = q
		c.mu.Unlock()
		return q, nil
	}

	c.mu.Lock()
	last := c.last
	c.mu.Unlock()
	return last, err
}

// Last returns the cached quote without fetching.
func (c *Client) Last() Quote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *Client) fetch(ctx context.Context) (Quote, error) {
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Quote{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Quote{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Quote{}, fmt.Errorf("quote: %s", resp.Status)
	}

	// Cap the read: this is a remote endpoint and the response is two short
	// strings. Without a limit a misbehaving upstream could stream forever
	// into a 466MB board.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Quote{}, err
	}

	// zenquotes returns an array of one: [{"q":"...","a":"...","h":"..."}].
	var arr []struct {
		Q string `json:"q"`
		A string `json:"a"`
	}
	if err := json.Unmarshal(body, &arr); err != nil {
		return Quote{}, fmt.Errorf("quote: %w", err)
	}
	if len(arr) == 0 || strings.TrimSpace(arr[0].Q) == "" {
		return Quote{}, fmt.Errorf("quote: empty response")
	}
	return Quote{
		Text:   strings.TrimSpace(arr[0].Q),
		Author: strings.TrimSpace(arr[0].A),
	}, nil
}
