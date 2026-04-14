package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// brokerClient provides HTTP access to the host-side mittens broker.
type brokerClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// newBrokerClient creates a client from the MITTENS_BROKER_* env vars.
// Returns nil if no broker is configured.
func newBrokerClient(cfg *config) *brokerClient {
	if !cfg.hasBroker() {
		return nil
	}

	c := &brokerClient{token: cfg.BrokerToken}

	if cfg.BrokerSock != "" {
		c.baseURL = "http://broker"
		c.httpClient = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", cfg.BrokerSock)
				},
			},
		}
	} else {
		c.baseURL = fmt.Sprintf("http://host.docker.internal:%s", cfg.BrokerPort)
		c.httpClient = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				// Bypass any proxy for broker communication.
				Proxy: nil,
			},
		}
	}
	return c
}

func (b *brokerClient) do(req *http.Request) (*http.Response, error) {
	if b.token != "" {
		req.Header.Set("X-Mittens-Token", b.token)
	}
	return b.httpClient.Do(req)
}

// get performs a GET request and returns the response body as a string.
func (b *brokerClient) get(path string) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, b.baseURL+path, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := b.do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode, nil
}

// getWithTimeout performs a GET request with a custom timeout, returning the
// response body as a string. Use for long-poll endpoints like /await-callback/.
func (b *brokerClient) getWithTimeout(path string, timeout time.Duration) (string, int, error) {
	req, err := http.NewRequest(http.MethodGet, b.baseURL+path, nil)
	if err != nil {
		return "", 0, err
	}
	if b.token != "" {
		req.Header.Set("X-Mittens-Token", b.token)
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: b.httpClient.Transport,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.StatusCode, nil
}

// post sends a POST request with the given content type and body.
func (b *brokerClient) post(path, contentType, body string) (int, error) {
	_, code, err := b.postWithBody(path, contentType, body)
	return code, err
}

// postWithBody sends a POST request and returns the response body.
func (b *brokerClient) postWithBody(path, contentType, body string) (string, int, error) {
	req, err := http.NewRequest(http.MethodPost, b.baseURL+path, strings.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := b.do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return string(respBody), resp.StatusCode, nil
}

// put sends a PUT request with JSON body and returns the HTTP status code.
func (b *brokerClient) put(path, jsonBody string) (int, error) {
	req, err := http.NewRequest(http.MethodPut, b.baseURL+path, strings.NewReader(jsonBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}
