package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// RelayPollClient polls a remote store-and-forward relay with a bounded
// outbound request lifetime. Credentials are used only in the request header.
type RelayPollClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewRelayPollClient(baseURL, token string) *RelayPollClient {
	return &RelayPollClient{baseURL: baseURL, token: token, client: &http.Client{Timeout: OutboundRequestTimeout}}
}

func (c *RelayPollClient) Poll(ctx context.Context, peerID string) ([]*RelayEnvelope, error) {
	pollCtx, cancel := context.WithTimeout(ctx, OutboundRequestTimeout)
	defer cancel()
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("transport: invalid relay endpoint")
	}
	u.Path = "/api/v1/transport/relay/poll"
	query := u.Query()
	query.Set("peer_id", peerID)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(pollCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("transport: relay poll status %d", resp.StatusCode)
	}
	var result struct {
		Envelopes []*RelayEnvelope `json:"envelopes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result.Envelopes, nil
}
