package hex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Helpers for integration handlers that call JSON APIs.

const maxUpstreamResponseBytes = 32 << 20

// NewJSONRequest builds a request with an optional JSON body.
func NewJSONRequest(ctx context.Context, method, url string, body any) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

// DoJSON sends the request and decodes a successful JSON response into
// result, which may be nil. Failures the app can act on become an
// IntegrationError: 400, 404, 409, 422 and 429 keep their status, and 401
// and 403 report that the service refused access. Other failures are
// returned as plain errors, which the API reports as 502.
func DoJSON(client *http.Client, request *http.Request, service string, result any) error {
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s request: %w", service, err)
	}
	defer closeReader(response.Body, service+" response")

	body, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read %s response: %w", service, err)
	}
	if len(body) > maxUpstreamResponseBytes {
		return fmt.Errorf("%s response exceeds %d bytes", service, maxUpstreamResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return UpstreamError(service, response.StatusCode, body)
	}
	if result == nil || len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fmt.Errorf("decode %s response: %w", service, err)
	}
	return nil
}

// UpstreamError translates a third-party error status for the app.
func UpstreamError(service string, status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 300 {
		detail = detail[:300] + "…"
	}
	message := fmt.Sprintf("%s answered %d", service, status)
	if detail != "" {
		message += ": " + detail
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return &IntegrationError{Status: http.StatusBadRequest, Message: message}
	case http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests:
		return &IntegrationError{Status: status, Message: message}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &IntegrationError{Status: http.StatusForbidden, Message: service + " refused access"}
	default:
		return fmt.Errorf("%s", message)
	}
}
