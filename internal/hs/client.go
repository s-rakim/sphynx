// Package hs is a small client for the Headscale REST API (/api/v1).
package hs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrUnauthorized is returned when Headscale rejects the API key.
var ErrUnauthorized = errors.New("headscale rejected the API key")

// Client talks to Headscale's REST gateway.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	mu  sync.RWMutex
	key string
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: httpClient}
}

func (c *Client) SetKey(k string) {
	c.mu.Lock()
	c.key = k
	c.mu.Unlock()
}

func (c *Client) Key() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.key
}

// APIError carries Headscale's error message back to the UI.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("headscale: %s (HTTP %d)", e.Message, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		return &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Types mirror headscale.v1 protobuf messages as rendered by grpc-gateway.

type User struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"createdAt"`
	DisplayName string    `json:"displayName,omitempty"`
	Email       string    `json:"email,omitempty"`
	ProviderID  string    `json:"providerId,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	ProfilePic  string    `json:"profilePicUrl,omitempty"`
}

type PreAuthKey struct {
	User       *User     `json:"user,omitempty"`
	ID         string    `json:"id"`
	Key        string    `json:"key"`
	Reusable   bool      `json:"reusable"`
	Ephemeral  bool      `json:"ephemeral"`
	Used       bool      `json:"used"`
	Expiration time.Time `json:"expiration"`
	CreatedAt  time.Time `json:"createdAt"`
	ACLTags    []string  `json:"aclTags"`
}

type Node struct {
	ID              string      `json:"id"`
	MachineKey      string      `json:"machineKey"`
	NodeKey         string      `json:"nodeKey"`
	DiscoKey        string      `json:"discoKey"`
	IPAddresses     []string    `json:"ipAddresses"`
	Name            string      `json:"name"`
	User            *User       `json:"user,omitempty"`
	LastSeen        *time.Time  `json:"lastSeen,omitempty"`
	Expiry          *time.Time  `json:"expiry,omitempty"`
	PreAuthKey      *PreAuthKey `json:"preAuthKey,omitempty"`
	CreatedAt       time.Time   `json:"createdAt"`
	RegisterMethod  string      `json:"registerMethod"`
	GivenName       string      `json:"givenName"`
	Online          bool        `json:"online"`
	ApprovedRoutes  []string    `json:"approvedRoutes"`
	AvailableRoutes []string    `json:"availableRoutes"`
	SubnetRoutes    []string    `json:"subnetRoutes"`
	Tags            []string    `json:"tags"`
}

type APIKey struct {
	ID         string     `json:"id"`
	Prefix     string     `json:"prefix"`
	Expiration *time.Time `json:"expiration,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastSeen   *time.Time `json:"lastSeen,omitempty"`
}

type Policy struct {
	Policy    string     `json:"policy"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Health checks Headscale's unauthenticated /health endpoint.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Message: resp.Status}
	}
	return nil
}

// Users

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var r struct {
		Users []User `json:"users"`
	}
	err := c.do(ctx, "GET", "/user", nil, nil, &r)
	return r.Users, err
}

func (c *Client) CreateUser(ctx context.Context, name, displayName, email string) (*User, error) {
	var r struct {
		User User `json:"user"`
	}
	err := c.do(ctx, "POST", "/user", nil, map[string]string{
		"name": name, "displayName": displayName, "email": email,
	}, &r)
	return &r.User, err
}

func (c *Client) RenameUser(ctx context.Context, id, newName string) error {
	return c.do(ctx, "POST", "/user/"+url.PathEscape(id)+"/rename/"+url.PathEscape(newName), nil, nil, nil)
}

func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/user/"+url.PathEscape(id), nil, nil, nil)
}

// Nodes

func (c *Client) ListNodes(ctx context.Context) ([]Node, error) {
	var r struct {
		Nodes []Node `json:"nodes"`
	}
	err := c.do(ctx, "GET", "/node", nil, nil, &r)
	return r.Nodes, err
}

func (c *Client) GetNode(ctx context.Context, id string) (*Node, error) {
	var r struct {
		Node Node `json:"node"`
	}
	err := c.do(ctx, "GET", "/node/"+url.PathEscape(id), nil, nil, &r)
	return &r.Node, err
}

func (c *Client) DeleteNode(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/node/"+url.PathEscape(id), nil, nil, nil)
}

// ExpireNode expires the node key now, or disables expiry when disable is true.
func (c *Client) ExpireNode(ctx context.Context, id string, disable bool) error {
	q := url.Values{}
	if disable {
		q.Set("disableExpiry", "true")
	}
	return c.do(ctx, "POST", "/node/"+url.PathEscape(id)+"/expire", q, nil, nil)
}

func (c *Client) RenameNode(ctx context.Context, id, name string) error {
	return c.do(ctx, "POST", "/node/"+url.PathEscape(id)+"/rename/"+url.PathEscape(name), nil, nil, nil)
}

func (c *Client) SetTags(ctx context.Context, id string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	return c.do(ctx, "POST", "/node/"+url.PathEscape(id)+"/tags", nil, map[string]any{"tags": tags}, nil)
}

func (c *Client) SetApprovedRoutes(ctx context.Context, id string, routes []string) error {
	if routes == nil {
		routes = []string{}
	}
	return c.do(ctx, "POST", "/node/"+url.PathEscape(id)+"/approve_routes", nil, map[string]any{"routes": routes}, nil)
}

// RegisterNode registers a pending node by its registration key (legacy flow).
func (c *Client) RegisterNode(ctx context.Context, user, key string) (*Node, error) {
	var r struct {
		Node Node `json:"node"`
	}
	q := url.Values{"user": {user}, "key": {key}}
	err := c.do(ctx, "POST", "/node/register", q, nil, &r)
	return &r.Node, err
}

// Interactive auth (tailscale up → /register/<auth id>)

func (c *Client) AuthRegister(ctx context.Context, user, authID string) (*Node, error) {
	var r struct {
		Node Node `json:"node"`
	}
	err := c.do(ctx, "POST", "/auth/register", nil, map[string]string{"user": user, "authId": authID}, &r)
	return &r.Node, err
}

func (c *Client) AuthApprove(ctx context.Context, authID string) error {
	return c.do(ctx, "POST", "/auth/approve", nil, map[string]string{"authId": authID}, nil)
}

func (c *Client) AuthReject(ctx context.Context, authID string) error {
	return c.do(ctx, "POST", "/auth/reject", nil, map[string]string{"authId": authID}, nil)
}

// Pre-auth keys

func (c *Client) ListPreAuthKeys(ctx context.Context) ([]PreAuthKey, error) {
	var r struct {
		Keys []PreAuthKey `json:"preAuthKeys"`
	}
	err := c.do(ctx, "GET", "/preauthkey", nil, nil, &r)
	return r.Keys, err
}

type CreateKeyRequest struct {
	User       string    `json:"user,omitempty"` // numeric user id; empty for tag-only keys
	Reusable   bool      `json:"reusable"`
	Ephemeral  bool      `json:"ephemeral"`
	Expiration time.Time `json:"expiration"`
	ACLTags    []string  `json:"aclTags,omitempty"`
}

func (c *Client) CreatePreAuthKey(ctx context.Context, req CreateKeyRequest) (*PreAuthKey, error) {
	var r struct {
		Key PreAuthKey `json:"preAuthKey"`
	}
	err := c.do(ctx, "POST", "/preauthkey", nil, req, &r)
	return &r.Key, err
}

func (c *Client) ExpirePreAuthKey(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/preauthkey/expire", nil, map[string]string{"id": id}, nil)
}

func (c *Client) DeletePreAuthKey(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/preauthkey", url.Values{"id": {id}}, nil, nil)
}

// API keys

func (c *Client) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	var r struct {
		Keys []APIKey `json:"apiKeys"`
	}
	err := c.do(ctx, "GET", "/apikey", nil, nil, &r)
	return r.Keys, err
}

func (c *Client) CreateAPIKey(ctx context.Context, exp time.Time) (string, error) {
	var r struct {
		Key string `json:"apiKey"`
	}
	err := c.do(ctx, "POST", "/apikey", nil, map[string]any{"expiration": exp}, &r)
	return r.Key, err
}

func (c *Client) ExpireAPIKey(ctx context.Context, prefix string) error {
	return c.do(ctx, "POST", "/apikey/expire", nil, map[string]string{"prefix": prefix}, nil)
}

func (c *Client) DeleteAPIKey(ctx context.Context, prefix string) error {
	return c.do(ctx, "DELETE", "/apikey/"+url.PathEscape(prefix), nil, nil, nil)
}

// Policy

func (c *Client) GetPolicy(ctx context.Context) (*Policy, error) {
	var p Policy
	err := c.do(ctx, "GET", "/policy", nil, nil, &p)
	return &p, err
}

func (c *Client) SetPolicy(ctx context.Context, policy string) (*Policy, error) {
	var p Policy
	err := c.do(ctx, "PUT", "/policy", nil, map[string]string{"policy": policy}, &p)
	return &p, err
}

func (c *Client) CheckPolicy(ctx context.Context, policy string) error {
	return c.do(ctx, "POST", "/policy/check", nil, map[string]string{"policy": policy}, nil)
}
