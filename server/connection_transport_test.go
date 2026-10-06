package hex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/oauth2"
)

func TestCredentialHTTPDestinations(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationCalls.Add(1)
		fmt.Fprint(w, "unexpected destination")
	}))
	defer destination.Close()
	var originCalls atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret-marker" {
			t.Error("allowed origin did not receive the bearer token")
		}
		switch r.URL.Path {
		case "/same":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/cross":
			http.Redirect(w, r, strings.Replace(destination.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
		case "/downgrade":
			http.Redirect(w, r, destination.URL, http.StatusFound)
		default:
			fmt.Fprint(w, "ok")
		}
	}))
	defer origin.Close()
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, origin.Client())
	connector := Connector{Name: "docs", OAuth2: oauth2.Config{Endpoint: oauth2.Endpoint{TokenURL: origin.URL + "/token"}},
		APIOrigins: []string{origin.URL, destination.URL, strings.Replace(destination.URL, "127.0.0.1", "localhost", 1)}}
	client, err := connectorHTTPClient(ctx, connector, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "secret-marker"}))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(origin.URL + "/same")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	for _, path := range []string{"/cross", "/downgrade"} {
		response, err := client.Get(origin.URL + path)
		if response != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Fatalf("unsafe redirect %s followed", path)
		}
	}
	connector.APIOrigins = []string{origin.URL}
	client, err = connectorHTTPClient(ctx, connector, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "secret-marker"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(destination.URL); err == nil {
		t.Fatal("unlisted initial destination accepted")
	}
	if destinationCalls.Load() != 0 || originCalls.Load() != 4 {
		t.Fatalf("unexpected calls origin=%d destination=%d", originCalls.Load(), destinationCalls.Load())
	}
	// Omitted origins allow only the OAuth token endpoint's origin.
	connector.APIOrigins = nil
	client, err = connectorHTTPClient(ctx, connector, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "secret-marker"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(destination.URL); err == nil {
		t.Fatal("default origin restriction bypassed")
	}
}

func TestConnectorRejectsInvalidAPIOrigins(t *testing.T) {
	for _, value := range []string{"http://remote.example", "https://user:password@api.example", "https://api.example/path", "https://api.example?token=x", "https://api.example#fragment", "//api.example"} {
		registry := new(IntegrationRegistry)
		err := registry.RegisterConnector(Connector{Name: "docs", Title: "Docs", APIOrigins: []string{value},
			OAuth2: oauth2.Config{ClientID: "client", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.test/authorize", TokenURL: "https://auth.test/token"}}})
		if err == nil {
			t.Fatalf("invalid origin %q accepted", value)
		}
	}
}
