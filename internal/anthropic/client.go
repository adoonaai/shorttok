package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const DefaultVersion = "2023-06-01"

// Headers forwarded from the client to the upstream API.
var passHeaders = []string{"x-api-key", "authorization", "anthropic-version", "anthropic-beta"}

// Client talks to the upstream Anthropic API.
type Client struct {
	BaseURL string
	APIKey  string // fallback credentials when the caller sends none
	HTTP    *http.Client
}

// Headers builds upstream headers from the incoming request headers.
func (c *Client) Headers(in http.Header) http.Header {
	out := make(http.Header)
	for _, h := range passHeaders {
		if v := in.Values(h); len(v) > 0 {
			out[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
		}
	}
	if out.Get("x-api-key") == "" && out.Get("authorization") == "" && c.APIKey != "" {
		out.Set("x-api-key", c.APIKey)
	}
	if out.Get("anthropic-version") == "" {
		out.Set("anthropic-version", DefaultVersion)
	}
	out.Set("content-type", "application/json")
	return out
}

// Send POSTs a raw body to the upstream path. The caller owns resp.Body.
func (c *Client) Send(ctx context.Context, path string, body []byte, hdr http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header = hdr.Clone()
	return c.HTTP.Do(req)
}

// Complete makes a simple non-streaming single-turn call and returns the text.
// Used by optimizers for side tasks such as summarization.
func (c *Client) Complete(ctx context.Context, hdr http.Header, model, system, prompt string, maxTokens int) (string, Usage, error) {
	body, err := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", Usage{}, err
	}
	h := hdr.Clone()
	h.Del("anthropic-beta") // the client's betas may not apply to the side model

	resp, err := c.Send(ctx, "/v1/messages", body, h)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("upstream %s: %s", resp.Status, clip(data, 300))
	}
	var r struct {
		Content []Block `json:"content"`
		Usage   Usage   `json:"usage"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", Usage{}, err
	}
	var sb strings.Builder
	for _, b := range r.Content {
		if b.Type() == "text" {
			sb.WriteString(b.Text())
		}
	}
	return sb.String(), r.Usage, nil
}

func clip(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
