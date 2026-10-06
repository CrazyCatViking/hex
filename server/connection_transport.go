package hex

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
)

func connectorOrigins(connector Connector) (map[string]bool, error) {
	values := connector.APIOrigins
	if len(values) == 0 {
		values = []string{connector.OAuth2.Endpoint.TokenURL}
	}
	origins := make(map[string]bool, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || !safeCredentialURL(parsed) || (len(connector.APIOrigins) > 0 &&
			(parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/")) {
			return nil, fmt.Errorf("connector %s has an invalid API origin %q", connector.Name, value)
		}
		origins[credentialOrigin(parsed)] = true
	}
	return origins, nil
}

func safeCredentialURL(value *url.URL) bool {
	return value != nil && value.Hostname() != "" && value.User == nil && value.Opaque == "" &&
		(value.Scheme == "https" || value.Scheme == "http" && loopbackHost(value.Hostname()))
}

func credentialOrigin(value *url.URL) string {
	return value.Scheme + "://" + strings.ToLower(value.Host)
}

// Validate outside oauth2.Transport: it attaches credentials on every request,
// including redirected requests whose Authorization header net/http removed.
type credentialDestinationTransport struct {
	base    http.RoundTripper
	origins map[string]bool
}

func (t credentialDestinationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !safeCredentialURL(r.URL) || !t.origins[credentialOrigin(r.URL)] {
		return nil, fmt.Errorf("connected-account request destination is not an allowed API origin")
	}
	return t.base.RoundTrip(r)
}

func connectorHTTPClient(ctx context.Context, connector Connector, source oauth2.TokenSource) (*http.Client, error) {
	client := oauth2.NewClient(ctx, source)
	origins, err := connectorOrigins(connector)
	if err != nil {
		return nil, err
	}
	client.Transport = credentialDestinationTransport{base: client.Transport, origins: origins}
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many connected-account redirects")
		}
		if len(via) == 0 || credentialOrigin(r.URL) != credentialOrigin(via[0].URL) {
			return fmt.Errorf("cross-origin connected-account redirects are not allowed")
		}
		return nil
	}
	return client, nil
}
