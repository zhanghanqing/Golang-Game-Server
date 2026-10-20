package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

// NodeStatus is the JSON returned by a game node's /game/status and /game/drain.
type NodeStatus struct {
	Node     string `json:"node"`
	Players  int    `json:"players"`
	Draining bool   `json:"draining"`
	Stopping bool   `json:"stopping"`
}

// NodeClient talks to the admin port of game nodes.
type NodeClient interface {
	Status(ctx context.Context, podIP string) (NodeStatus, error)
	Drain(ctx context.Context, podIP string) (NodeStatus, error)
}

// HTTPNodeClient calls the game node admin endpoints over HTTP.
type HTTPNodeClient struct {
	Port   int
	Client *http.Client
}

// NewHTTPNodeClient creates a client for the admin port (9100 in the generated config).
func NewHTTPNodeClient(port int) *HTTPNodeClient {
	return &HTTPNodeClient{Port: port, Client: &http.Client{Timeout: 3 * time.Second}}
}

func (c *HTTPNodeClient) Status(ctx context.Context, podIP string) (NodeStatus, error) {
	return c.do(ctx, http.MethodGet, podIP, "/game/status")
}

func (c *HTTPNodeClient) Drain(ctx context.Context, podIP string) (NodeStatus, error) {
	return c.do(ctx, http.MethodPost, podIP, "/game/drain")
}

func (c *HTTPNodeClient) do(ctx context.Context, method, podIP, path string) (NodeStatus, error) {
	var st NodeStatus
	url := "http://" + net.JoinHostPort(podIP, strconv.Itoa(c.Port)) + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return st, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("%s %s: %s", method, url, resp.Status)
	}
	return st, json.NewDecoder(resp.Body).Decode(&st)
}
