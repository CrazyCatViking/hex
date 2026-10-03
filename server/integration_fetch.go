package hex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// ReadIntegrationJSON helps approved adapters bound upstream response bodies.
// The adapter owns the fixed destination, credentials, field selection and
// upstream row/cost limits. Redirects are rejected so credentials stay at that
// destination. Raw upstream failures are never included in returned errors.
func ReadIntegrationJSON(ctx context.Context, client *http.Client, request *http.Request, maxBytes int64) (json.RawMessage, error) {
	if maxBytes < 1 || maxBytes > 1<<20 {
		return nil, errors.New("upstream JSON budget must be 1 byte–1 MiB")
	}
	if client == nil || request == nil {
		return nil, errors.New("upstream request and HTTP client are required")
	}
	bounded := *client
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := bounded.Do(request.Clone(ctx))
	if err != nil {
		return nil, errors.New("upstream request failed")
	}
	defer closeReader(response.Body, "integration upstream response")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("upstream returned a non-success status")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, errors.New("upstream response could not be read")
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("upstream response exceeds its byte budget")
	}
	if !json.Valid(data) {
		return nil, errors.New("upstream response is not JSON")
	}
	return json.RawMessage(data), nil
}
