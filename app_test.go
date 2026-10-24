package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const testAdminToken = "test-admin-token"

func TestProxyPreservesPathQueryMethodAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(body))
	}))
	defer upstream.Close()

	application := newTestApp(t, upstream.URL)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/items/42?expand=owner", strings.NewReader("payload"))
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got, want := response.Body.String(), "POST /api/v1/items/42?expand=owner payload"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

func TestHoneypotRoutesTakePrecedenceOverProxy(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	application := newTestAppWithPayloads(t, upstream.URL, map[string][]byte{"/.env": []byte("fake fixture")}, testAdminToken)
	request := httptest.NewRequest(http.MethodGet, "/.env", nil)
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "fake fixture" {
		t.Fatalf("response = %d %q, want 200 fake fixture", response.Code, response.Body.String())
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestTraversalIsRejectedBeforeProxy(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
	}))
	defer upstream.Close()

	application := newTestApp(t, upstream.URL)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.URL.Path = "/../../.env"
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
	}
}

func TestProxyWithoutUpstreamReturnsStableAPIError(t *testing.T) {
	application := newTestApp(t, "")
	request := httptest.NewRequest(http.MethodGet, "/api/resource", nil)
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	var body apiError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.ErrorCode != "UPSTREAM_NOT_CONFIGURED" {
		t.Fatalf("error_code = %q, want UPSTREAM_NOT_CONFIGURED", body.ErrorCode)
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusRequest.Header.Set("Authorization", "Bearer "+testAdminToken)
	statusRecorder := httptest.NewRecorder()
	application.ServeHTTP(statusRecorder, statusRequest)
	var status statusResponse
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.ProxiedHits != 1 {
		t.Fatalf("proxied_hits = %d, want 1", status.ProxiedHits)
	}
}

func TestConfigAPIUsesAuthValidationAndConditionalUpdates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Path)
	}))
	defer upstream.Close()

	application := newTestApp(t, "")
	unauthorized := httptest.NewRecorder()
	application.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	getConfig := httptest.NewRequest(http.MethodGet, "/v1/config", nil)
	getConfig.Header.Set("Authorization", "Bearer "+testAdminToken)
	configRecorder := httptest.NewRecorder()
	application.ServeHTTP(configRecorder, getConfig)
	if configRecorder.Code != http.StatusOK {
		t.Fatalf("config status = %d, want %d", configRecorder.Code, http.StatusOK)
	}
	var config configResponse
	if err := json.Unmarshal(configRecorder.Body.Bytes(), &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}

	invalid := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(`{"upstream_url":"file:///etc/passwd"}`))
	invalid.Header.Set("Authorization", "Bearer "+testAdminToken)
	invalid.Header.Set("Content-Type", "application/json")
	invalid.Header.Set("If-Match", `"`+config.Revision+`"`)
	invalidResponse := httptest.NewRecorder()
	application.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid config status = %d, want %d", invalidResponse.Code, http.StatusUnprocessableEntity)
	}
	var invalidBody map[string]any
	if err := json.Unmarshal(invalidResponse.Body.Bytes(), &invalidBody); err != nil {
		t.Fatalf("decode invalid response: %v", err)
	}
	extra, ok := invalidBody["extra"].(map[string]any)
	if !ok {
		t.Fatal("invalid response has no extra object")
	}
	validationErrors, ok := extra["validation_errors"].([]any)
	if !ok || len(validationErrors) == 0 {
		t.Fatalf("validation_errors = %#v, want non-empty array", extra["validation_errors"])
	}

	update := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(`{"upstream_url":"`+upstream.URL+`"}`))
	update.Header.Set("Authorization", "Bearer "+testAdminToken)
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("If-Match", `"`+config.Revision+`"`)
	updateResponse := httptest.NewRecorder()
	application.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d: %s", updateResponse.Code, http.StatusOK, updateResponse.Body.String())
	}
	var updated configResponse
	if err := json.Unmarshal(updateResponse.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated config: %v", err)
	}
	if updated.Revision == config.Revision || updated.UpstreamURL != upstream.URL {
		t.Fatalf("updated config = %#v, want new revision and upstream %q", updated, upstream.URL)
	}

	staleUpdate := httptest.NewRequest(http.MethodPut, "/v1/config", strings.NewReader(`{"upstream_url":"`+upstream.URL+`"}`))
	staleUpdate.Header.Set("Authorization", "Bearer "+testAdminToken)
	staleUpdate.Header.Set("Content-Type", "application/json")
	staleUpdate.Header.Set("If-Match", `"`+config.Revision+`"`)
	staleResponse := httptest.NewRecorder()
	application.ServeHTTP(staleResponse, staleUpdate)
	if staleResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status = %d, want %d", staleResponse.Code, http.StatusPreconditionFailed)
	}
}

func TestAdminDisabledWithoutToken(t *testing.T) {
	application := newTestAppWithPayloads(t, "", nil, "")
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	request.Header.Set("Authorization", "Bearer some-token")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestDashboardServesEmbeddedAssetsWithSecurityHeaders(t *testing.T) {
	application := newTestApp(t, "")
	for _, path := range []string{"/dashboard/", "/dashboard/app.js", "/dashboard/app.css"} {
		response := httptest.NewRecorder()
		application.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s missing Content-Security-Policy", path)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", path, response.Header().Get("Cache-Control"))
		}
	}
}

func newTestApp(t *testing.T, upstream string) *app {
	return newTestAppWithPayloads(t, upstream, nil, testAdminToken)
}

func newTestAppWithPayloads(t *testing.T, upstream string, payloads map[string][]byte, adminToken string) *app {
	t.Helper()
	metrics, err := openMetricsStore(filepath.Join(t.TempDir(), "metrics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := metrics.Close(); err != nil {
			t.Errorf("close test metrics database: %v", err)
		}
	})
	application, err := newApp(payloads, adminToken, upstream, metrics)
	if err != nil {
		t.Fatal(err)
	}
	return application
}
