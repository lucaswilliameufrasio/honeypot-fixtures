package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerOnlyServesExactAllowlistedPaths(t *testing.T) {
	application := newTestAppWithPayloads(t, "", map[string][]byte{
		"/.env": []byte("fake fixture"),
	}, testAdminToken)

	tests := []struct {
		name       string
		method     string
		path       string
		statusCode int
		body       string
	}{
		{name: "allowlisted fixture", method: http.MethodGet, path: "/.env", statusCode: http.StatusOK, body: "fake fixture"},
		{name: "traversal path", method: http.MethodGet, path: "/../../.env", statusCode: http.StatusNotFound, body: "404 page not found\n"},
		{name: "head request", method: http.MethodHead, path: "/.env", statusCode: http.StatusOK},
		{name: "unsupported method", method: http.MethodPost, path: "/.env", statusCode: http.StatusMethodNotAllowed, body: "{\"message\":\"Método não permitido\",\"error_code\":\"METHOD_NOT_ALLOWED\"}\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/", nil)
			request.URL.Path = test.path
			response := httptest.NewRecorder()

			application.ServeHTTP(response, request)

			if response.Code != test.statusCode {
				t.Fatalf("status = %d, want %d", response.Code, test.statusCode)
			}
			if response.Body.String() != test.body {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.body)
			}
			if test.statusCode == http.StatusOK && response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want %q", response.Header().Get("Cache-Control"), "no-store")
			}
		})
	}
}

func TestConfiguredListenAddressIsLoopbackOnly(t *testing.T) {
	tests := []struct {
		value   string
		want    string
		wantErr bool
	}{
		{value: "", want: "127.0.0.1:8089"},
		{value: "127.0.0.1:8089", want: "127.0.0.1:8089"},
		{value: "[::1]:8089", want: "[::1]:8089"},
		{value: "0.0.0.0:8089", wantErr: true},
		{value: "127.0.0.1:70000", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := configuredListenAddress(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error for non-loopback or invalid address")
				}
				if strings.Contains(err.Error(), test.value) {
					t.Fatal("error should not echo the configured value")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("address = %q, want %q", got, test.want)
			}
		})
	}
}
