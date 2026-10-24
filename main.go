package main

import (
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "os"
    "path"
    "path/filepath"
    "strings"
)

func main() {
    data, err := os.ReadFile("routes.json")
    if err != nil { log.Fatal(err) }
    routes := map[string]string{}
    if err := json.Unmarshal(data, &routes); err != nil { log.Fatal(err) }

    // All response bytes are loaded through a fixed allowlist on startup.
    payloads := make(map[string][]byte, len(routes))
    for route, fixture := range routes {
        if strings.Contains(fixture, "/") || strings.Contains(fixture, "..") { log.Fatalf("invalid fixture: %q", fixture) }
        payloads[route], err = os.ReadFile(filepath.Join("fixtures", fixture))
        if err != nil { log.Fatal(err) }
    }

    handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/" { http.NotFound(w, r); return } // Never replace actual homepage.
        if r.Method != http.MethodGet && r.Method != http.MethodHead { w.WriteHeader(http.StatusMethodNotAllowed); return }
        urlPath := r.URL.Path
        body, ok := payloads[urlPath]
        if !ok && (strings.Contains(urlPath, "/../") || strings.Contains(urlPath, "/./")) {
            body, ok = payloads[path.Clean(urlPath)]
        }
        if !ok { http.NotFound(w, r); return }
        switch {
        case strings.HasSuffix(urlPath, ".json") || urlPath == "/env" || urlPath == "/actuator/env": w.Header().Set("Content-Type", "application/json; charset=utf-8")
        case strings.HasSuffix(urlPath, ".html") || strings.HasSuffix(urlPath, ".php") || urlPath == "/debug/default/view": w.Header().Set("Content-Type", "text/html; charset=utf-8")
        default: w.Header().Set("Content-Type", "text/plain; charset=utf-8")
        }
        w.Header().Set("Cache-Control", "no-store")
        w.Header().Set("X-Robots-Tag", "noindex, nofollow")
        // In production, use structured logging, trusted client-IP chain, and rate limits.
        log.Printf("honeypot path=%q method=%s remote=%q ua=%q", r.URL.EscapedPath(), r.Method, r.RemoteAddr, r.UserAgent())
        w.WriteHeader(http.StatusOK)
        if r.Method == http.MethodGet { _, _ = w.Write(body) }
    })
    fmt.Println("Honeypot listening on localhost:8089")
    log.Fatal(http.ListenAndServe("127.0.0.1:8089", handler))
}
