// Command gen writes synthetic generic JSONL to stdout for fixtures and
// benchmarks: about 250-byte lines, a fixed seed, timestamps spread over
// the last 24 hours.
//
//	go run ./internal/gen -bytes 100000000 > /tmp/big.jsonl
package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"time"
)

var (
	routes   = []string{"/api/users", "/api/orders", "/api/search", "/health", "/api/login", "/api/items/42"}
	methods  = []string{"GET", "GET", "GET", "POST", "PUT", "DELETE"}
	clients  = []string{"acme", "globex", "initech", "umbrella", "hooli", "stark", "wayne"}
	services = []string{"api", "worker", "gateway"}
	errors   = []string{"upstream timeout", "connection reset by peer", "invalid token", "rate limited"}
	warns    = []string{"slow query", "retrying request", "cache miss storm"}
)

func main() {
	nBytes := flag.Int64("bytes", 10_000_000, "approximate output size in bytes")
	seed := flag.Int64("seed", 42, "random seed")
	flag.Parse()

	rng := rand.New(rand.NewSource(*seed))
	w := bufio.NewWriterSize(os.Stdout, 1<<20)
	defer w.Flush()

	now := time.Now().UTC()
	var written int64
	for i := 0; written < *nBytes; i++ {
		ts := now.Add(-time.Duration(rng.Int63n(int64(24 * time.Hour))))
		client := clients[rng.Intn(len(clients))]
		service := services[rng.Intn(len(services))]
		reqID := fmt.Sprintf("%08x-%04x-%04x", rng.Uint32(), rng.Intn(1<<16), rng.Intn(1<<16))
		userID := fmt.Sprintf("u%05d", rng.Intn(50000))
		dur := rng.ExpFloat64() * 40
		var level, msg string
		switch r := rng.Float64(); {
		case r < 0.03:
			level = "error"
			msg = errors[rng.Intn(len(errors))]
		case r < 0.10:
			level = "warn"
			msg = warns[rng.Intn(len(warns))]
		case r < 0.15:
			level = "debug"
			msg = "cache lookup " + reqID
		default:
			level = "info"
			msg = fmt.Sprintf("%s %s %d %.0fms", methods[rng.Intn(len(methods))], routes[rng.Intn(len(routes))], 200+rng.Intn(3)*100, dur)
		}
		line := fmt.Sprintf(`{"timestamp":"%s","level":"%s","message":"%s","service":"%s","requestId":"%s","userId":"%s","durationMs":%.2f,"client":"%s","fields":{"region":"eu-west-%d","pod":"%s-%d"}}`+"\n",
			ts.Format(time.RFC3339Nano), level, msg, service, reqID, userID, dur, client, rng.Intn(3), service, rng.Intn(8))
		n, err := w.WriteString(line)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		written += int64(n)
	}
}
