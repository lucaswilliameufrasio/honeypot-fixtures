package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

const maxConfigBodyBytes = 4096
const maxRecentEvents = 20

type app struct {
	payloads   map[string][]byte
	adminToken string
	startedAt  time.Time
	metrics    *metricsStore

	configMu sync.RWMutex
	upstream *url.URL
	revision string
}

type requestEvent struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
}

type statusResponse struct {
	UptimeSeconds      int64          `json:"uptime_seconds"`
	UpstreamConfigured bool           `json:"upstream_configured"`
	HoneypotHits       uint64         `json:"honeypot_hits"`
	ProxiedHits        uint64         `json:"proxied_hits"`
	RecentEvents       []requestEvent `json:"recent_events"`
}

type configResponse struct {
	UpstreamURL string `json:"upstream_url,omitempty"`
	Revision    string `json:"revision"`
}

type configRequest struct {
	UpstreamURL string `json:"upstream_url"`
}

type validationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type apiError struct {
	Message   string         `json:"message"`
	ErrorCode string         `json:"error_code"`
	Extra     map[string]any `json:"extra,omitempty"`
}

func newApp(payloads map[string][]byte, adminToken string, upstreamValue string, metrics *metricsStore) (*app, error) {
	if metrics == nil {
		return nil, errors.New("metrics store is required")
	}
	var upstream *url.URL
	if upstreamValue != "" {
		parsed, err := parseUpstream(upstreamValue)
		if err != nil {
			return nil, fmt.Errorf("invalid HONEYPOT_UPSTREAM: %w", err)
		}
		upstream = parsed
	}

	revision, err := newRevision()
	if err != nil {
		return nil, fmt.Errorf("create config revision: %w", err)
	}

	return &app{
		payloads:   payloads,
		adminToken: adminToken,
		startedAt:  time.Now(),
		metrics:    metrics,
		upstream:   upstream,
		revision:   revision,
	}, nil
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeMethodNotAllowed(w, http.MethodGet, http.MethodHead)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "ok")
		}
		return
	case "/dashboard":
		http.Redirect(w, r, "/dashboard/", http.StatusPermanentRedirect)
		return
	case "/dashboard/":
		a.serveDashboard(w, r)
		return
	case "/dashboard/app.js":
		a.serveDashboardScript(w, r)
		return
	case "/dashboard/app.css":
		a.serveDashboardStyle(w, r)
		return
	case "/v1/status":
		a.serveStatus(w, r)
		return
	case "/v1/config":
		a.serveConfig(w, r)
		return
	}

	if hasTraversalSegment(r.URL.Path) {
		http.NotFound(w, r)
		return
	}

	if body, ok := a.payloads[r.URL.Path]; ok {
		a.serveHoneypot(w, r, body)
		return
	}

	a.proxyRequest(w, r)
}

func (a *app) serveDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeMethodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setDashboardSecurityHeaders(w)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(dashboardHTML)
	}
}

func (a *app) serveDashboardScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeMethodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	setDashboardSecurityHeaders(w)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(dashboardScript)
	}
}

func (a *app) serveDashboardStyle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeMethodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	setDashboardSecurityHeaders(w)
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(dashboardStyle)
	}
}

func (a *app) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !a.authorize(w, r) {
		return
	}

	metrics, err := a.metrics.Snapshot(r.Context())
	if err != nil {
		log.Printf("metrics status read failed: %T", err)
		writeError(w, http.StatusServiceUnavailable, "As métricas estão temporariamente indisponíveis", "METRICS_UNAVAILABLE", nil)
		return
	}
	a.configMu.RLock()
	upstreamConfigured := a.upstream != nil
	a.configMu.RUnlock()
	response := statusResponse{
		UptimeSeconds:      int64(time.Since(a.startedAt).Seconds()),
		UpstreamConfigured: upstreamConfigured,
		HoneypotHits:       metrics.HoneypotHits,
		ProxiedHits:        metrics.ProxyHits,
		RecentEvents:       metrics.RecentEvents,
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *app) serveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPut)
		return
	}
	if !a.authorize(w, r) {
		return
	}

	if r.Method == http.MethodGet {
		a.writeConfig(w)
		return
	}
	a.updateConfig(w, r)
}

func (a *app) writeConfig(w http.ResponseWriter) {
	a.configMu.RLock()
	response := configResponse{Revision: a.revision}
	if a.upstream != nil {
		response.UpstreamURL = a.upstream.String()
	}
	w.Header().Set("ETag", `"`+a.revision+`"`)
	a.configMu.RUnlock()
	writeJSON(w, http.StatusOK, response)
}

func (a *app) updateConfig(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Content-Type inválido", "MALFORMED_REQUEST", nil)
		return
	}
	if mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type deve ser application/json", "UNSUPPORTED_MEDIA_TYPE", nil)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxConfigBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request configRequest
	if err := decoder.Decode(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "O corpo da requisição excede o limite", "PAYLOAD_TOO_LARGE", nil)
			return
		}
		writeError(w, http.StatusBadRequest, "JSON inválido", "MALFORMED_REQUEST", nil)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "Envie um único objeto JSON", "MALFORMED_REQUEST", nil)
		return
	}
	upstream, err := parseUpstream(request.UpstreamURL)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "URL upstream inválida", "INVALID_PARAMS", map[string]any{
			"validation_errors": []validationError{{Field: "upstream_url", Message: err.Error()}},
		})
		return
	}

	a.configMu.Lock()
	currentETag := `"` + a.revision + `"`
	if r.Header.Get("If-Match") == "" || r.Header.Get("If-Match") != currentETag {
		currentRevision := a.revision
		a.configMu.Unlock()
		writeError(w, http.StatusPreconditionFailed, "A configuração mudou; recarregue antes de salvar", "PRECONDITION_FAILED", map[string]any{
			"current_revision": currentRevision,
		})
		return
	}
	revision, err := newRevision()
	if err != nil {
		a.configMu.Unlock()
		writeError(w, http.StatusInternalServerError, "Não foi possível atualizar a configuração", "UNEXPECTED_ERROR", nil)
		return
	}
	a.upstream = upstream
	a.revision = revision
	a.configMu.Unlock()
	a.writeConfig(w)
}

func (a *app) authorize(w http.ResponseWriter, r *http.Request) bool {
	if a.adminToken == "" {
		writeError(w, http.StatusServiceUnavailable, "A administração está desabilitada", "ADMIN_DISABLED", nil)
		return false
	}
	const prefix = "Bearer "
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		writeError(w, http.StatusUnauthorized, "Token administrativo obrigatório", "UNAUTHORIZED", nil)
		return false
	}
	provided := strings.TrimPrefix(authorization, prefix)
	if subtle.ConstantTimeCompare([]byte(provided), []byte(a.adminToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "Token administrativo inválido", "UNAUTHORIZED", nil)
		return false
	}
	return true
}

func (a *app) serveHoneypot(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		a.recordEvent("honeypot", r, http.StatusMethodNotAllowed)
		writeMethodNotAllowed(w, http.MethodGet, http.MethodHead)
		return
	}
	a.recordEvent("honeypot", r, http.StatusOK)
	urlPath := r.URL.Path
	switch {
	case strings.HasSuffix(urlPath, ".json") || urlPath == "/env" || urlPath == "/actuator/env":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case strings.HasSuffix(urlPath, ".html") || strings.HasSuffix(urlPath, ".php") || urlPath == "/debug/default/view":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

func (a *app) proxyRequest(w http.ResponseWriter, r *http.Request) {
	a.configMu.RLock()
	var target *url.URL
	if a.upstream != nil {
		copy := *a.upstream
		target = &copy
	}
	a.configMu.RUnlock()
	if target == nil {
		a.recordEvent("proxy", r, http.StatusServiceUnavailable)
		writeError(w, http.StatusServiceUnavailable, "Nenhum upstream foi configurado", "UPSTREAM_NOT_CONFIGURED", nil)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ModifyResponse = func(response *http.Response) error {
		a.recordEvent("proxy", r, response.StatusCode)
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("upstream request failed: %v", err)
		a.recordEvent("proxy", r, http.StatusBadGateway)
		writeError(w, http.StatusBadGateway, "O upstream não respondeu", "UPSTREAM_UNAVAILABLE", nil)
	}
	proxy.ServeHTTP(w, r)
}

func (a *app) recordEvent(kind string, r *http.Request, status int) {
	event := requestEvent{
		Time:   time.Now().UTC(),
		Kind:   kind,
		Method: r.Method,
		Path:   r.URL.Path,
		Status: status,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.metrics.Record(ctx, event); err != nil {
		log.Printf("metrics event persistence failed: %T", err)
	}
}

func parseUpstream(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.New("sintaxe de URL inválida")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("use uma URL absoluta http ou https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("não use credenciais, query, fragmento ou caminho base na URL")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func hasTraversalSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func newRevision() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, "Método não permitido", "METHOD_NOT_ALLOWED", nil)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string, code string, extra map[string]any) {
	writeJSON(w, status, apiError{Message: message, ErrorCode: code, Extra: extra})
}

func setDashboardSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
}
