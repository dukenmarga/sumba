package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

var (
	n           *int64
	c           *int64
	maxRequests uint64

	url *string

	stepConnection = int64(maxRequests / 10)
	done           = make(chan struct{})
)

func init() {
	n = flag.Int64("n", 100, "number of requests to perform")
	c = flag.Int64("c", 10, "number of concurrent workers")
	url = flag.String("url", "http://127.0.0.1", "URL string")
}

func main() {
	flag.Parse()
	maxRequests = uint64(*n)
	fmt.Printf("Test %d requests using %d workers to: %v\n", maxRequests, *c, *url)

	ctx := context.Background()

	// counter to keep track of number of requests
	reqCounter := NewCounterChannel(0)

	// Monitor each routine and wait them until desired number of requests is reached
	workers := sync.WaitGroup{}
	for i := int64(0); i < *c; i++ {
		workers.Add(1)

		// Unique http client will be used and reused for 1 routine
		client := http.DefaultTransport

		// This go routine will start sending requests sequentially one after each request is completed
		go sendRequests(ctx, client, url, i, reqCounter, &workers)
	}
	workers.Wait()
}

// Tell the client to send request sequentially until maxRequests is reached
// Each client will not depend on each other and has its own request timeline.
func sendRequests(_ctx context.Context, client http.RoundTripper, url *string, workerNumber int64, reqCounter *ChannelCounter, wg *sync.WaitGroup) {
	// done is to indicate that a request has got response
	done := make(chan bool)
	defer wg.Done()

	for {
		nRequests := reqCounter.Read()
		if nRequests > maxRequests {
			break
		}
		reqCounter.Add(1)

		// We can send request synchronously, but we will use go routine for further operation
		go request(client, url, done)
		<-done
	}

}

// Send a http request to specified url using a specified client and trace
// the request time
func request(client http.RoundTripper, url *string, done chan bool) {
	req, _ := http.NewRequest("GET", *url, nil)

	var start, connect, dnsStart, tlsHandshake time.Time
	var requestTime, connectTime, dnsQueryTime, tlsHandshakeTime, totalTime time.Duration

	trace := &httptrace.ClientTrace{
		// Measure DNS lookup time
		DNSStart: func(dsi httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(ddi httptrace.DNSDoneInfo) {
			dnsQueryTime = time.Since(dnsStart)
			fmt.Printf("DNS Query time: %v\n", dnsQueryTime)
		},

		// Measure TLS Handshake time
		TLSHandshakeStart: func() { tlsHandshake = time.Now() },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			tlsHandshakeTime = time.Since(tlsHandshake)
			fmt.Printf("TLS Handshake time: %v\n", tlsHandshakeTime)
		},

		// Measure Connect time to server
		ConnectStart: func(network, addr string) { connect = time.Now() },
		ConnectDone: func(network, addr string, err error) {
			connectTime = time.Since(connect)
			fmt.Printf("Connect time: %v\n", connectTime)
		},

		// Measure time to get the first byte
		GotFirstResponseByte: func() {
			requestTime = time.Since(start)
			fmt.Printf("Time from start to first byte: %v\n", requestTime)
		},
	}

	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	start = time.Now()
	if _, err := client.RoundTrip(req); err != nil {
		log.Println(err)
	}
	totalTime = time.Since(start)
	fmt.Printf("Total time: %v\n\n", totalTime)

	done <- true
}

type ChannelCounter struct {
	ch     chan func()
	number uint64
}

// Initialise the channel counter
func NewCounterChannel(start uint64) *ChannelCounter {
	// Number of maximum operations, e.g. calling Add or Read will be considered 1 operation
	// Each worker that run sendRequest() will have 1 Add and 1 Read (total 2 operation), means
	// total maximum worker is approximately 1024/2 = 512 workers
	maxOperation := 1024

	// initialise counter, start from 0
	counter := &ChannelCounter{make(chan func(), maxOperation), start}

	// this go routine will be run in the background to watch every operation added to the channel counter
	// and then run it sequentially
	go func(counter *ChannelCounter) {
		for f := range counter.ch {
			f()
		}
	}(counter)

	// return the channel counter object
	return counter
}

// Add counter number
func (c *ChannelCounter) Add(num uint64) {
	c.ch <- func() {
		c.number = c.number + num
	}
}

// Read counter number
func (c *ChannelCounter) Read() uint64 {
	ret := make(chan uint64)
	c.ch <- func() {
		ret <- c.number
		close(ret)
	}
	return <-ret
}
