//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// apiClient talks to a running app.
type apiClient struct {
	base  string
	token string
	http  *http.Client
}

// newClient builds a client from the flags, the environment, or the default
// address.
func newClient(args []string) *apiClient {
	base := os.Getenv("COMPUTER_USE_URL")
	if base == "" {
		base = "http://" + defaultListen
	}
	if v, ok := flagValue(args, "url"); ok && v != "" {
		base = v
	}
	token := os.Getenv("COMPUTER_USE_TOKEN")
	if v, ok := flagValue(args, "token"); ok {
		token = v
	}
	return &apiClient{base: strings.TrimSuffix(base, "/"), token: token, http: &http.Client{Timeout: 120 * time.Second}}
}

// do sends a request and decodes the JSON answer into out.
func (c *apiClient) do(method, path string, body, out any) error {
	raw, err := c.raw(method, path, body)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unexpected answer from %s: %w", c.base, err)
		}
	}
	return nil
}

// raw sends a request and returns the body.
func (c *apiClient) raw(method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the app at %s: %w (start `computer-use run`)", c.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
