package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const maxRate = 5000
const maxDuration = 10 * time.Minute

type result struct {
	Mode             string         `json:"mode"`
	Rate             int            `json:"offered_rate_rps"`
	Workers          int            `json:"workers"`
	Duration         float64        `json:"scheduled_duration_seconds"`
	WallDuration     float64        `json:"wall_duration_seconds"`
	Offered          uint64         `json:"offered"`
	Started          uint64         `json:"started"`
	Completed        uint64         `json:"completed"`
	Dropped          uint64         `json:"dropped"`
	Errors           uint64         `json:"errors"`
	StatusCounts     map[string]int `json:"status_counts"`
	ActualOfferedRPS float64        `json:"actual_offered_rps"`
	DeliveredRPS     float64        `json:"delivered_rps"`
	LatencyP50MS     float64        `json:"latency_p50_ms"`
	LatencyP95MS     float64        `json:"latency_p95_ms"`
	LatencyP99MS     float64        `json:"latency_p99_ms"`
	LatencyMaxMS     float64        `json:"latency_max_ms"`
	NetworkErrors    uint64         `json:"network_errors"`
}

type sample struct {
	status  int
	latency time.Duration
	err     bool
}

func main() {
	var target string
	var mode string
	var rate int
	var duration time.Duration
	var workers int
	var queueSize int
	flag.StringVar(&target, "url", "", "Loopback HTTP server URL")
	flag.StringVar(&mode, "mode", "mixed", "honeypot, proxy, or mixed")
	flag.IntVar(&rate, "rate", 100, "Open-loop offered requests per second")
	flag.DurationVar(&duration, "duration", 10*time.Second, "Scheduled load duration")
	flag.IntVar(&workers, "workers", 128, "Concurrent HTTP workers")
	flag.IntVar(&queueSize, "queue", 1024, "Bounded pending request queue")
	flag.Parse()
	if err := validate(target, mode, rate, duration, workers, queueSize); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(target, mode, rate, duration, workers, queueSize, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func validate(target string, mode string, rate int, duration time.Duration, workers int, queueSize int) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed == nil || parsed.Scheme != "http" || parsed.Host == "" {
		return errors.New("-url must be an absolute loopback HTTP URL")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return errors.New("-url must be a loopback origin without credentials, query, or base path")
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		host = parsed.Hostname()
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("loadgen only targets loopback addresses")
		}
	}
	if mode != "honeypot" && mode != "proxy" && mode != "mixed" {
		return errors.New("-mode must be honeypot, proxy, or mixed")
	}
	if rate < 1 || rate > maxRate {
		return fmt.Errorf("-rate must be between 1 and %d", maxRate)
	}
	if duration < time.Second || duration > maxDuration {
		return fmt.Errorf("-duration must be between 1s and %s", maxDuration)
	}
	if workers < 1 || workers > 512 {
		return errors.New("-workers must be between 1 and 512")
	}
	if queueSize < 1 || queueSize > 8192 {
		return errors.New("-queue must be between 1 and 8192")
	}
	return nil
}

func run(target string, mode string, rate int, duration time.Duration, workers int, queueSize int, output io.Writer) error {
	baseURL, err := url.Parse(target)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		Proxy:                 nil,
		MaxIdleConns:          workers,
		MaxIdleConnsPerHost:   workers,
		MaxConnsPerHost:       workers,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer transport.CloseIdleConnections()

	work := make(chan int, queueSize)
	results := make(chan sample, workers*2)
	var started atomic.Uint64
	var dropped atomic.Uint64
	var workerGroup sync.WaitGroup
	for workerIndex := 0; workerIndex < workers; workerIndex++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for sequence := range work {
				started.Add(1)
				requestPath := pathFor(mode, sequence)
				request, err := http.NewRequest(http.MethodGet, baseURL.Scheme+"://"+baseURL.Host+requestPath, nil)
				if err != nil {
					results <- sample{err: true}
					continue
				}
				requestStart := time.Now()
				response, err := client.Do(request)
				if err != nil {
					results <- sample{latency: time.Since(requestStart), err: true}
					continue
				}
				_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
				_ = response.Body.Close()
				results <- sample{status: response.StatusCode, latency: time.Since(requestStart), err: readErr != nil}
			}
		}()
	}

	var samples []sample
	collectorDone := make(chan struct{})
	go func() {
		for item := range results {
			samples = append(samples, item)
		}
		close(collectorDone)
	}()

	interval := time.Second / time.Duration(rate)
	scheduledStart := time.Now()
	scheduledEnd := scheduledStart.Add(duration)
	var offered uint64
	for sequence := 0; ; sequence++ {
		due := scheduledStart.Add(time.Duration(sequence) * interval)
		if !due.Before(scheduledEnd) {
			break
		}
		if wait := time.Until(due); wait > 0 {
			time.Sleep(wait)
		}
		offered++
		select {
		case work <- sequence:
		default:
			dropped.Add(1)
		}
	}
	close(work)
	workerGroup.Wait()
	close(results)
	<-collectorDone

	wallDuration := time.Since(scheduledStart)
	result := summarize(mode, rate, workers, duration, wallDuration, offered, started.Load(), dropped.Load(), samples)
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func pathFor(mode string, sequence int) string {
	switch mode {
	case "honeypot":
		return "/.env"
	case "proxy":
		return "/__bench/echo?sample=" + fmt.Sprint(sequence)
	default:
		if sequence%5 == 0 {
			return "/__bench/echo?sample=" + fmt.Sprint(sequence)
		}
		return "/.env"
	}
}

func summarize(mode string, rate int, workers int, scheduled time.Duration, wall time.Duration, offered uint64, started uint64, dropped uint64, samples []sample) result {
	latencies := make([]time.Duration, 0, len(samples))
	statusCounts := make(map[string]int)
	var errorsCount uint64
	var networkErrors uint64
	for _, item := range samples {
		latencies = append(latencies, item.latency)
		if item.err {
			errorsCount++
			networkErrors++
			continue
		}
		statusCounts[fmt.Sprint(item.status)]++
		if item.status < http.StatusOK || item.status >= http.StatusMultipleChoices {
			errorsCount++
		}
	}
	sort.Slice(latencies, func(i int, j int) bool { return latencies[i] < latencies[j] })
	quantile := func(p float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		index := int(math.Ceil(float64(len(latencies))*p)) - 1
		if index < 0 {
			index = 0
		}
		return float64(latencies[index]) / float64(time.Millisecond)
	}
	maxLatency := 0.0
	if len(latencies) > 0 {
		maxLatency = float64(latencies[len(latencies)-1]) / float64(time.Millisecond)
	}
	wallSeconds := wall.Seconds()
	if wallSeconds <= 0 {
		wallSeconds = scheduled.Seconds()
	}
	return result{
		Mode:             mode,
		Rate:             rate,
		Workers:          workers,
		Duration:         scheduled.Seconds(),
		WallDuration:     wallSeconds,
		Offered:          offered,
		Started:          started,
		Completed:        uint64(len(samples)),
		Dropped:          dropped,
		Errors:           errorsCount,
		StatusCounts:     statusCounts,
		ActualOfferedRPS: float64(offered) / scheduled.Seconds(),
		DeliveredRPS:     float64(len(samples)) / wallSeconds,
		LatencyP50MS:     quantile(0.50),
		LatencyP95MS:     quantile(0.95),
		LatencyP99MS:     quantile(0.99),
		LatencyMaxMS:     maxLatency,
		NetworkErrors:    networkErrors,
	}
}
