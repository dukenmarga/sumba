package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
)

var (
	n           *int64
	c           *int64
	emulate     *bool
	maxRequests uint64

	C   *string
	url *string
	m   *string
	p   *string

	mu sync.Mutex
)

func init() {
	n = flag.Int64("n", 100, "number of requests to perform")
	c = flag.Int64("c", 10, "number of concurrent workers")
	emulate = flag.Bool("emulate", false, "emulate headless browser")
	url = flag.String("url", "http://127.0.0.1", "URL string")
	C = flag.String("C", "", "cookie in the form of \"key1=value1;key2=value2\"")
	m = flag.String("m", "GET", "HTTP method: GET (default), POST (set -p for POST-file)")
	p = flag.String("p", "", "POST-file, containing payload for POST method. Use -T to define type")
}

func main() {
	flag.Parse()
	maxRequests = uint64(*n)
	fmt.Println("Sumba - Simple Server Benchmark Tool")
	fmt.Printf("====================================\n\n")
	fmt.Printf("Target\t%v\n", *url)
	fmt.Printf("Total requests\t\t\t%4.0d\n", maxRequests)
	fmt.Printf("Worker used\t\t\t%4.0d\n", *c)

	ctx := context.Background()

	// counter to keep track of number of requests
	reqCounter := NewCounterChannel(0)

	// tracker to monitor each request
	reqsTracker := NewRequestTracker()

	// Request Data
	reqData := RequestData{
		Method:   *m,
		Cookies:  parseCookie(*C),
		PostFile: *p,
	}

	// Monitor each routine and wait them until desired number of requests is reached
	workers := sync.WaitGroup{}
	for i := int64(0); i < *c; i++ {
		workers.Add(1)

		if *emulate {
			// Launch headless browser
			browser := rod.New().MustConnect()
			defer browser.MustClose()

			go sendRequestsHeadless(ctx, browser, url, i, reqCounter, reqsTracker, &workers)
		} else {
			// Unique http client will be used and reused for 1 routine
			client := http.DefaultTransport
			// This go routine will start sending requests sequentially one after each request is completed
			go sendRequests(ctx, client, url, i, reqCounter, reqsTracker, reqData, &workers)
		}

	}
	workers.Wait()

	AverageFirstByteTime(reqsTracker)
	AverageTotalTime(reqsTracker)

	RequestPerSecond(reqsTracker, maxRequests)

}

func parseCookie(C string) []http.Cookie {
	httpCookie := []http.Cookie{}

	for item := range strings.SplitSeq(C, ";") {
		item = strings.TrimSpace(item)
		keyValue := strings.Split(item, "=")
		if len(keyValue) != 2 {
			continue
		}
		httpCookie = append(httpCookie, http.Cookie{
			Name:  keyValue[0],
			Value: keyValue[1],
		})
	}
	return httpCookie
}

// Tell the client to send request sequentially until maxRequests is reached
// Each client will not depend on each other and has its own request timeline.
func sendRequests(_ctx context.Context,
	client http.RoundTripper,
	url *string,
	workerNumber int64,
	reqCounter *ChannelCounter,
	reqTracker *[]RequestTracker,
	reqData RequestData,
	wg *sync.WaitGroup) {

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
		go request(client, url, reqTracker, reqData, done)
		<-done
	}
}

// Send a http request to specified url using a specified client and trace
// the request time
func request(client http.RoundTripper, url *string, reqsTracker *[]RequestTracker, reqData RequestData, done chan bool) {
	payload := &bytes.Buffer{}
	if reqData.Method == "POST" {
		payloadBytes, err := readFile(reqData.PostFile)
		if err != nil {
			log.Fatal(err)
		}
		payload = bytes.NewBuffer(payloadBytes)
	}
	req, err := http.NewRequest(reqData.Method, *url, payload)
	if err != nil {
		log.Printf("NewRequest: %v", err)
	}

	var start, connect, dnsStart, tlsHandshake time.Time
	var firstByteTime, connectTime, dnsQueryTime, tlsHandshakeTime, totalTime time.Duration

	reqTrack := RequestTracker{}
	trace := &httptrace.ClientTrace{
		// Measure DNS lookup time
		DNSStart: func(dsi httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(ddi httptrace.DNSDoneInfo) {
			dnsQueryTime = time.Since(dnsStart)
			reqTrack.dnsQueryTime = float64(dnsQueryTime / time.Microsecond)
		},

		// Measure TLS Handshake time
		TLSHandshakeStart: func() { tlsHandshake = time.Now() },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			tlsHandshakeTime = time.Since(tlsHandshake)
			reqTrack.tlsHandshakeTime = float64(tlsHandshakeTime / time.Microsecond)
		},

		// Measure Connect time to server
		ConnectStart: func(network, addr string) { connect = time.Now() },
		ConnectDone: func(network, addr string, err error) {
			connectTime = time.Since(connect)
			reqTrack.connectTime = float64(connectTime / time.Microsecond)
		},

		// Measure time to get the first byte
		GotFirstResponseByte: func() {
			firstByteTime = time.Since(start)
			reqTrack.firstByteTime = float64(firstByteTime / time.Microsecond)
		},

		GotConn: func(info httptrace.GotConnInfo) {
			// fmt.Printf("Connection reused: %v\n", info.Reused)
		},
	}

	// New request
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	// Add cookies
	for _, cookie := range reqData.Cookies {
		req.AddCookie(&cookie)
	}

	// Start the request
	start = time.Now()
	if _, err := client.RoundTrip(req); err != nil {
		log.Println(err)
	}
	totalTime = time.Since(start)
	reqTrack.totalTime = float64(totalTime / time.Microsecond)

	mu.Lock()
	defer mu.Unlock()
	*reqsTracker = append(*reqsTracker, reqTrack)

	done <- true
}

func requestHeadless(browser *rod.Browser, url *string, reqsTracker *[]RequestTracker, done chan bool) {
	var start time.Time
	var totalTime time.Duration

	reqTrack := RequestTracker{}

	start = time.Now()
	_ = browser.MustPage(*url).MustWaitLoad()
	totalTime = time.Since(start)
	reqTrack.totalTime = float64(totalTime / time.Microsecond)

	mu.Lock()
	defer mu.Unlock()
	*reqsTracker = append(*reqsTracker, reqTrack)

	done <- true
}

// Send request sequentially using headless browser until maxRequests is reached.
// Each client will not depend on each other and has its own request timeline.
func sendRequestsHeadless(
	_ctx context.Context,
	browser *rod.Browser,
	url *string,
	workerNumber int64,
	reqCounter *ChannelCounter,
	reqTracker *[]RequestTracker,
	wg *sync.WaitGroup) {

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
		go requestHeadless(browser, url, reqTracker, done)
		<-done
	}
}

type RequestData struct {
	Method   string
	Cookies  []http.Cookie
	PostFile string
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

type RequestTracker struct {
	ch               chan func()
	dnsQueryTime     float64
	connectTime      float64
	tlsHandshakeTime float64
	firstByteTime    float64
	totalTime        float64
}

// Initialise the request tracker
func NewRequestTracker() *[]RequestTracker {
	req := &[]RequestTracker{}
	return req
}

// Compute average first byte time
func AverageFirstByteTime(r *[]RequestTracker) float64 {
	sum := SumFirstByteTime(r)
	average := sum / float64(len(*r))

	averageFormatted, unit := formatDurationUnit(average)
	fmt.Printf("Average first byte time\t\t%4.0f %s\n", averageFormatted, unit)

	return average
}

// Compute average request time
func AverageTotalTime(r *[]RequestTracker) float64 {
	sum := SumTotalTime(r)
	average := sum / float64(len(*r))

	averageFormatted, unit := formatDurationUnit(average)
	fmt.Printf("Average time per request\t%4.0f %s\n", averageFormatted, unit)

	return average
}

// Sum up the first byte time for all requests from all workers in microsecond
func SumFirstByteTime(r *[]RequestTracker) float64 {
	sum := 0.0
	for _, reqTrack := range *r {
		sum += reqTrack.firstByteTime
	}
	return sum
}

// Sum up the total request time for all requests from all workers in microsecond
func SumTotalTime(r *[]RequestTracker) float64 {
	sum := 0.0
	for _, reqTrack := range *r {
		sum += reqTrack.totalTime
	}
	return sum
}

// Calculates server benchmark as request per second
func RequestPerSecond(r *[]RequestTracker, maxRequests uint64) float64 {
	sum := SumTotalTime(r)
	// Request per second = total request / total time
	// Total time is in microseconds
	rps := float64(maxRequests) / sum * 1000000
	if rps < 0 {
		fmt.Printf("Request per second\t\t%4.3f req/s\n", rps)
	} else {
		fmt.Printf("Request per second\t\t%4.0f req/s\n", rps)
	}
	return rps
}

func readFile(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("empty file path")
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return data, nil
}

// Format t (in microsecond) into readable duration format, depending on the value
// e.g t=1234567 => t=1.234567 s or t=1234 => t=1.234 ms
func formatDurationUnit(t float64) (float64, string) {
	if t > 1000 && t < 1000000 {
		return t / 1000, "ms"
	}
	if t >= 1000000 {
		return t / 1000000, "s"
	}
	return t, "μs"
}
