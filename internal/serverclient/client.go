// Package serverclient is the authenticated HTTP client used by the local agc
// CLI to synchronize consensus and record context deliveries.
package serverclient

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
	"time"

	"github.com/agent-consensus/ac/internal/api"
	"github.com/agent-consensus/ac/internal/model"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type HTTPError struct {
	StatusCode int
	Status     string
	Message    string
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("server returned %s: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("server returned %s", e.Status)
}

func IsNotFound(err error) bool {
	var httpError *HTTPError
	return errors.As(err, &httpError) && httpError.StatusCode == http.StatusNotFound
}

func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("server URL must be an absolute http(s) URL")
	}
	return &Client{
		baseURL:    baseURL,
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Sync publishes the calling repository's Decisions and Events. It cannot
// create organization Rules.
func (c *Client) Sync(ctx context.Context, organization string, request api.SyncRequest) (api.SyncResponse, error) {
	var response api.SyncResponse
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/organizations/"+url.PathEscape(organization)+"/sync", request, &response)
	return response, err
}

func (c *Client) SubmitPromotion(ctx context.Context, organization string, request api.SubmitPromotionRequest) (model.Promotion, error) {
	var promotion model.Promotion
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/organizations/"+url.PathEscape(organization)+"/promotions", request, &promotion)
	return promotion, err
}

func (c *Client) SubmitProposal(ctx context.Context, organization string, request api.SubmitProposalRequest) (model.Proposal, error) {
	var proposal model.Proposal
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/organizations/"+url.PathEscape(organization)+"/proposals", request, &proposal)
	return proposal, err
}

func (c *Client) SubmitEvent(ctx context.Context, organization string, request api.SubmitEventRequest) (model.Event, error) {
	var event model.Event
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/organizations/"+url.PathEscape(organization)+"/events", request, &event)
	return event, err
}

func (c *Client) RecordContext(ctx context.Context, organization string, input api.ContextRecordInput) (api.ContextRecord, error) {
	var record api.ContextRecord
	err := c.doJSON(ctx, http.MethodPost, "/api/v1/organizations/"+url.PathEscape(organization)+"/context-records", input, &record)
	return record, err
}

func (c *Client) Snapshot(ctx context.Context, organization, repository string) (api.SnapshotResponse, error) {
	var snapshot api.SnapshotResponse
	path := "/api/v1/organizations/" + url.PathEscape(organization) + "/snapshot"
	if strings.TrimSpace(repository) != "" {
		path += "?repository=" + url.QueryEscape(strings.TrimSpace(repository))
	}
	err := c.doJSON(ctx, http.MethodGet, path, nil, &snapshot)
	return snapshot, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestValue, responseValue any) error {
	var body io.Reader
	if requestValue != nil {
		encoded, err := json.Marshal(requestValue)
		if err != nil {
			return fmt.Errorf("encode server request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create server request: %w", err)
	}
	if requestValue != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "agc/0.4.0-dev")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("contact agc server: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			return fmt.Errorf("read server error: %w", readErr)
		}
		var apiError api.ErrorResponse
		if json.Unmarshal(body, &apiError) == nil && apiError.Error != "" {
			return &HTTPError{StatusCode: response.StatusCode, Status: response.Status, Message: apiError.Error}
		}
		return &HTTPError{StatusCode: response.StatusCode, Status: response.Status}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(responseValue); err != nil {
		return fmt.Errorf("decode server response: %w", err)
	}
	return nil
}
