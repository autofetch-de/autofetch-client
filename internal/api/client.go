package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RevokedError is returned when the server tells us the client no longer exists
// or has been disabled.
type RevokedError struct {
	Status int
	Msg    string
}

func (e *RevokedError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "client revoked or missing"
}

func IsRevoked(err error) bool {
	var r *RevokedError
	return errors.As(err, &r)
}

type Client struct {
	BaseURL     string
	ClientID    string
	ClientToken string
	HTTP        *http.Client
}

func New(baseURL, clientID, token string) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		ClientID:    strings.TrimSpace(clientID),
		ClientToken: token,
		HTTP:        &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) authHeader() (string, bool) {
	if strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientToken) == "" {
		return "", false
	}
	return "Client " + c.ClientID + ":" + c.ClientToken, true
}

func (c *Client) requireClientID() error {
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("client_id missing")
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in any, out any) (status int, err error) {
	var buf bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&buf).Encode(in); err != nil {
			return 0, err
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, &buf)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Authorization: Client <client_id>:<client_token_plain>
	if v, ok := c.authHeader(); ok {
		req.Header.Set("Authorization", v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	status = resp.StatusCode
	if out == nil {
		if status < 200 || status >= 300 {
			return status, fmt.Errorf("%s %s failed: status=%d", method, path, status)
		}
		return status, nil
	}

	if status < 200 || status >= 300 {
		var e APIErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&e) // best-effort
		if status == 403 && strings.EqualFold(e.Error, "CLIENT_REVOKED_OR_MISSING") {
			return status, &RevokedError{Status: status, Msg: "CLIENT_REVOKED_OR_MISSING"}
		}
		return status, fmt.Errorf("%s %s failed: status=%d body=%v", method, path, status, e)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return status, err
	}
	return status, nil
}

func (c *Client) GetRuntimeConfig(ctx context.Context) (*RuntimeConfigResponse, bool, error) {
	if err := c.requireClientID(); err != nil {
		return nil, false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/client/v1/config", nil)
	if err != nil {
		return nil, false, err
	}
	if v, ok := c.authHeader(); ok {
		req.Header.Set("Authorization", v)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode == http.StatusForbidden {
		var e APIErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if strings.EqualFold(e.Error, "CLIENT_REVOKED_OR_MISSING") {
			return nil, false, &RevokedError{Status: resp.StatusCode, Msg: "CLIENT_REVOKED_OR_MISSING"}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e APIErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, false, fmt.Errorf("GET /api/client/v1/config failed: status=%d body=%v", resp.StatusCode, e)
	}

	var out RuntimeConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, false, err
	}
	return &out, true, nil
}

func (c *Client) RegisterStart(ctx context.Context, req RegisterStartRequest) (*RegisterStartResponse, error) {
	var out RegisterStartResponse
	_, err := c.doJSON(ctx, http.MethodPost, "/api/client/v1/register/start", req, &out)
	return &out, err
}

func (c *Client) RegisterPoll(ctx context.Context, req RegisterPollRequest) (*RegisterPollResponse, error) {
	var out RegisterPollResponse
	_, err := c.doJSON(ctx, http.MethodPost, "/api/client/v1/register/poll", req, &out)
	return &out, err
}

// LeaseJob has NO clientID parameter on purpose.
// Body client_id is always derived from c.ClientID => cannot diverge from auth header.
func (c *Client) LeaseJob(ctx context.Context) (*LeaseResponse, error) {
	if err := c.requireClientID(); err != nil {
		return nil, err
	}

	var out LeaseResponse
	_, err := c.doJSON(
		ctx,
		http.MethodPost,
		"/api/client/v1/jobs/lease",
		LeaseRequest{ClientID: c.ClientID},
		&out,
	)
	return &out, err
}

// Harden: always force body client_id = header id
func (c *Client) DedupeClaim(ctx context.Context, req DedupeClaimRequest) (*DedupeClaimResponse, error) {
	if err := c.requireClientID(); err != nil {
		return nil, err
	}
	req.ClientID = c.ClientID

	var out DedupeClaimResponse
	_, err := c.doJSON(ctx, http.MethodPost, "/api/client/v1/dedupe/claim", req, &out)
	return &out, err
}

// Heartbeat is special: it must surface 409 job_canceled and other non-2xx states
// as structured data instead of hiding them inside a generic error string.
func (c *Client) Heartbeat(ctx context.Context, jobID string, req HeartbeatRequest) (code int, hb *HeartbeatResponse, apiErr *APIErrorResponse, err error) {
	if err := c.requireClientID(); err != nil {
		return 0, nil, nil, err
	}
	req.ClientID = c.ClientID

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(req); err != nil {
		return 0, nil, nil, err
	}

	path := fmt.Sprintf("/api/client/v1/jobs/%s/heartbeat", jobID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &buf)
	if err != nil {
		return 0, nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	if v, ok := c.authHeader(); ok {
		httpReq.Header.Set("Authorization", v)
	}

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()

	code = resp.StatusCode

	// 200 OK
	if code == 200 {
		var out HeartbeatResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return code, nil, nil, err
		}
		return code, &out, nil, nil
	}

	// Expected non-2xx JSON (e.g. 409 job_canceled, 409 attempt_not_open, 404 attempt_not_found)
	var e APIErrorResponse
	_ = json.NewDecoder(resp.Body).Decode(&e) // best-effort; body may be empty

	if code == 403 && strings.EqualFold(e.Error, "CLIENT_REVOKED_OR_MISSING") {
		return code, nil, &e, &RevokedError{Status: code, Msg: "CLIENT_REVOKED_OR_MISSING"}
	}

	return code, nil, &e, nil
}

// Harden: always force body client_id = header id
func (c *Client) Complete(ctx context.Context, jobID string, req CompleteRequest) (status int, err error) {
	if err := c.requireClientID(); err != nil {
		return 0, err
	}
	req.ClientID = c.ClientID

	return c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/client/v1/jobs/%s/complete", jobID), req, &struct{}{})
}
