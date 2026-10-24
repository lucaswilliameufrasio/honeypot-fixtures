package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	address := flag.String("addr", "127.0.0.1:18090", "loopback address for the isolated benchmark upstream")
	flag.Parse()
	if err := validateAddress(*address); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.URL.Path == "/__bench/health" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		_, _ = fmt.Fprintf(w, "%s %s", r.Method, r.URL.RequestURI())
	})
	server := &http.Server{
		Addr:              *address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "benchmark upstream failed")
		os.Exit(1)
	}
}

func validateAddress(value string) error {
	host, portText, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("-addr must be a loopback host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("-addr has an invalid port")
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("benchmark upstream must bind to loopback")
		}
	}
	return nil
}
