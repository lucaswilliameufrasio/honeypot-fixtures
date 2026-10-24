package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	data, err := os.ReadFile("routes.json")
	if err != nil {
		return fmt.Errorf("read routes: %w", err)
	}
	routes := map[string]string{}
	if err := json.Unmarshal(data, &routes); err != nil {
		return fmt.Errorf("decode routes: %w", err)
	}

	// All honeypot response bytes are loaded through a fixed allowlist on startup.
	payloads := make(map[string][]byte, len(routes))
	for route, fixture := range routes {
		if strings.Contains(fixture, "/") || strings.Contains(fixture, "..") {
			return fmt.Errorf("invalid fixture: %q", fixture)
		}
		payloads[route], err = os.ReadFile(filepath.Join("fixtures", fixture))
		if err != nil {
			return fmt.Errorf("read fixture for %q: %w", route, err)
		}
	}

	metrics, err := openMetricsStore(os.Getenv("HONEYPOT_DB_PATH"))
	if err != nil {
		return err
	}
	defer metrics.Close()

	application, err := newApp(payloads, os.Getenv("HONEYPOT_ADMIN_TOKEN"), os.Getenv("HONEYPOT_UPSTREAM"), metrics)
	if err != nil {
		return err
	}
	listenAddr, err := configuredListenAddress(os.Getenv("HONEYPOT_LISTEN_ADDR"))
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           application,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("honeypot listening on %s", server.Addr)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func configuredListenAddress(value string) (string, error) {
	if value == "" {
		return "127.0.0.1:8089", nil
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return "", fmt.Errorf("HONEYPOT_LISTEN_ADDR must be a loopback host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("HONEYPOT_LISTEN_ADDR has an invalid port")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("HONEYPOT_LISTEN_ADDR must use a loopback host")
		}
	}
	return value, nil
}
