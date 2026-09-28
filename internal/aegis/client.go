package aegis

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// EventResult is Aegis Cloud's answer for one event.
type EventResult struct {
	Key    string `json:"key"`
	Status string `json:"status"` // accepted, duplicate or rejected
	Reason string `json:"reason,omitempty"`
}

type reportResponse struct {
	Results    []EventResult `json:"results"`
	ServerTime string        `json:"serverTime"`
}

// cloudError is a report Aegis Cloud refused.
type cloudError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *cloudError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("Aegis Cloud answered %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("Aegis Cloud answered %d", e.Status)
}

// stopsReporting: the token was replaced or the server removed. Retrying
// cannot succeed until Aegis Agent is configured again.
func (e *cloudError) stopsReporting() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusGone
}

type client struct {
	http      *http.Client
	url       string
	token     string
	userAgent string
}

func newClient(url, token, agentVersion string) *client {
	return &client{
		http:      &http.Client{Timeout: 30 * time.Second},
		url:       url,
		token:     token,
		userAgent: "aegis-agent/" + agentVersion,
	}
}

// encode is the gzip-compressed JSON of a report, and its uncompressed size.
func encode(report Report) ([]byte, int, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, 0, err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return nil, 0, err
	}
	if err := writer.Close(); err != nil {
		return nil, 0, err
	}
	return compressed.Bytes(), len(raw), nil
}

func (c *client) send(ctx context.Context, report Report) (*reportResponse, error) {
	body, _, err := encode(report)
	if err != nil {
		return nil, fmt.Errorf("encode report: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("User-Agent", c.userAgent)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach Aegis Cloud: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read Aegis Cloud answer: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		failure := &cloudError{Status: response.StatusCode}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &body) == nil {
			failure.Code = body.Error.Code
			failure.Message = body.Error.Message
		}
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			failure.RetryAfter = time.Duration(seconds) * time.Second
		}
		return nil, failure
	}

	var result reportResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("read Aegis Cloud answer: %w", err)
	}
	return &result, nil
}
