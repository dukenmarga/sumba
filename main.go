package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
	maxRequests uint64

	C       *string
	url     *string
	m       *string
	p       *string
	E       *string
	skipTLS *bool
	classic *bool
	emulate *bool
	stress  *bool

	mu sync.Mutex

	// counter to keep track of number of requests
	reqCounter = NewCounterChannel(0)

	// tracker to monitor each request
	reqsTracker = NewRequestTracker()
)

func init() {
	n = flag.Int64("n", 0, "number of requests to perform")
	c = flag.Int64("c", 1, "number of concurrent workers")
	url = flag.String("url", "http://127.0.0.1", "URL string")
	C = flag.String("C", "", "cookie in the form of \"key1=value1;key2=value2\"")
	m = flag.String("m", "GET", "HTTP method: GET (default), POST (set -p for POST-file)")
	p = flag.String("p", "", "POST-file, containing payload for POST method. Use -T to define type")
	E = flag.String("E", "", "certificate file")
	skipTLS = flag.Bool("skipTLS", false, "skip TLS verification")

	classic = flag.Bool("classic", false, "classic benchmark")
	emulate = flag.Bool("emulate", false, "emulate headless browser")
	stress = flag.Bool("stress", false, "stress test")
}

func main() {
	flag.Parse()
	maxRequests = uint64(*n)

	showInitialInfo()

	ctx := context.Background()

	// Test connection
	err := testConnection()
	if err != nil {
		return
	}

	// Request Data
	reqData := RequestData{
		URL:      *url,
		Method:   *m,
		Cookies:  parseCookie(*C),
		PostFile: *p,
	}

	// Classic benchmark:
	// Send n requests from c workers concurrently
	if *classic {
		// Monitor each routine and wait them until desired number of requests is reached
		workers := sync.WaitGroup{}
		for i := int64(0); i < *c; i++ {
			// Unique http client will be used and reused for 1 routine
			client := NewHTTPClient(*skipTLS, *E)

			// This go routine will start sending requests sequentially one after each request is completed
			workers.Add(1)
			go sendRequests(ctx, client, reqData, &workers)

		}
		workers.Wait()
		AverageConnectTime(reqsTracker)
		AverageTotalTime(reqsTracker)
		RequestPerSecond(reqsTracker, maxRequests)

		return
	}

	// Emulate headless browser to benchmark:
	// Send n requests from c workers concurrently
	if *emulate {
		// Monitor each routine and wait them until desired number of requests is reached
		workers := sync.WaitGroup{}
		for i := int64(0); i < *c; i++ {
			// Launch headless browser
			browser := rod.New().MustConnect()
			defer browser.MustClose()

			workers.Add(1)
			go sendRequestsHeadless(ctx, browser, url, &workers)

		}
		workers.Wait()
		AverageConnectTime(reqsTracker)
		AverageTotalTime(reqsTracker)
		RequestPerSecond(reqsTracker, maxRequests)

		return
	}

	// Stress test:
	// Send requests incrementally (default: +10 requests per 3 second)
	// How:
	// - Determine number of rps and calculate time interval between each request
	// - For example: rps = 25, interval = 1s / 25 = 0.04s = 40ms / request
	// - Send request to queue every 40ms (technically to a channel as queue)
	// - Workers (default: 100) will pick up request from the channel and send it
	if *stress {
		// rps is the number of requests per second, initial value is 10
		rps := 5

		// max request per second, default is 300 for stress test
		if maxRequests == 0 {
			maxRequests = 300
		}

		// Holds the request queue
		reqQueue := make(chan struct{})

		// worker is the number of workers
		maxWorkers := 1000
		workers := make(chan struct{}, maxWorkers)

		// ticker is interval to increase rps
		tInc := 1
		ticker := time.NewTicker(time.Duration(tInc) * time.Second)
		defer ticker.Stop()

		// interval is the time interval between each request (in milliseconds)
		interval := time.NewTicker(time.Duration(1000/rps) * time.Millisecond)
		defer interval.Stop()

		wg := sync.WaitGroup{}

		// This routine will increase rps every 3 seconds
		ctxTicker, cancelTicker := context.WithCancel(context.Background())
		wg.Add(1)
		go func() {
			for range ticker.C {
				rps += 15
				fmt.Printf("Sending requests at\t%d RPS\n", rps)
				interval.Reset(time.Duration(1000000/rps) * time.Microsecond)
				if rps >= int(maxRequests) {
					cancelTicker()
					break
				}
			}
			defer wg.Done()
		}()

		// This routine will send request to queue every 'interval' time
		ctxInterval, cancelInterval := context.WithCancel(context.Background())
		wg.Add(1)
		go func() {
		loop:
			for {
				select {
				// If ticker is cancelled, we cancel the interval and exit the loop
				case <-ctxTicker.Done():
					cancelInterval()
					break loop // Exit the loop when context is cancelled
				case <-interval.C:
					reqQueue <- struct{}{}
				}
			}
			defer wg.Done()
		}()

		// This routine will pick up request from queue and send it
		wg.Add(1)
		go func() {
			doneReq := make(chan bool)
		loop:
			for {
				select {
				case <-ctxInterval.Done():
					break loop
				case <-reqQueue:
					// assign a worker to this request
					workers <- struct{}{}

					go func() {
						defer func() {
							// release the worker
							<-workers
						}()
						client := NewHTTPClient(*skipTLS, *E)
						reqCounter.Add(1)

						// Send request. We set the rps to categorise the request
						reqData.RPS = rps
						go requestWait(client, reqData, doneReq)
						<-doneReq
					}()
				}
			}
			defer wg.Done()
		}()

		wg.Wait()
		GenerateOutput(reqsTracker, maxRequests)
	}
}

// NewHTTPClient constructs an HTTP client with configurable TLS handling and conservative transport defaults.
func NewHTTPClient(skipTLS bool, certificateFile string) *http.Client {
	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			Proxy:             http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: skipTLS,
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		Timeout: 10 * time.Second,
	}
	if certificateFile != "" {
		caCertPool := x509.NewCertPool()
		caCertPool = readCertificate(certificateFile)
		client.Transport.(*http.Transport).TLSClientConfig.RootCAs = caCertPool
	}

	return client
}

// showInitialInfo prints a banner describing the benchmark target.
func showInitialInfo() {
	fmt.Println("Sumba - Simple Server Benchmark Tool")
	fmt.Printf("====================================\n\n")
	fmt.Printf("Target\t%v\n", *url)
}

// readCertificate loads a PEM-encoded certificate file and returns a cert pool containing it.
func readCertificate(path string) *x509.CertPool {
	// Load the self-signed certificate
	caCert, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("Error reading CA certificate: %v", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	return caCertPool
}

// parseCookie converts a semicolon-separated cookie string into individual http.Cookie entries.
// Invalid pairs are skipped and surrounding whitespace is trimmed.
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

// sendRequests instructs a worker to issue sequential HTTP requests until the shared maxRequests limit is reached.
// Each worker maintains its own pacing but coordinates through reqCounter to avoid overshooting.
func sendRequests(
	_ctx context.Context,
	client *http.Client,
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
		go requestWait(client, reqData, done)
		<-done
	}
}

// requestWait sends a request and notifies the provided channel once the response has been processed.
func requestWait(client *http.Client, reqData RequestData, done chan bool) {
	request(client, reqData)
	done <- true
}

// request performs the HTTP call and records detailed timing metrics for aggregation.
func request(client *http.Client, reqData RequestData) error {
	payload := &bytes.Buffer{}
	if reqData.Method == "POST" {
		payloadBytes, err := readFile(reqData.PostFile)
		if err != nil {
			log.Fatal(err)
		}
		payload = bytes.NewBuffer(payloadBytes)
	}
	req, err := http.NewRequest(reqData.Method, reqData.URL, payload)
	if err != nil {
		log.Printf("NewRequest: %v", err)
	}

	var connectStart, connectDone, dnsStart, dnsDone time.Time
	var gotConn, gotFirstResponseByte, tlsHandshakeStart, tlsHandshakeDone time.Time

	reqTrack := RequestTracker{}
	trace := &httptrace.ClientTrace{
		// Measure DNS lookup time
		DNSStart: func(dsi httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:  func(ddi httptrace.DNSDoneInfo) { dnsDone = time.Now() },

		// Measure Connect time to server
		ConnectStart: func(network, addr string) { connectStart = time.Now() },
		ConnectDone: func(network, addr string, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "Connect error: %v\n", err)
			}
			connectDone = time.Now()
		},

		// Measure time to get connection
		GotConn: func(info httptrace.GotConnInfo) { gotConn = time.Now() },

		// Measure time to get the first byte
		GotFirstResponseByte: func() { gotFirstResponseByte = time.Now() },

		// Measure TLS Handshake time
		TLSHandshakeStart: func() { tlsHandshakeStart = time.Now() },
		TLSHandshakeDone:  func(cs tls.ConnectionState, err error) { tlsHandshakeDone = time.Now() },
	}

	// New request
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	// Add cookies
	for _, cookie := range reqData.Cookies {
		req.AddCookie(&cookie)
	}

	// Start the request
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Read the body
	_, err = io.ReadAll(resp.Body)
	if err != nil {
		log.Println(err)
	}
	readBodyDone := time.Now()

	// DNS Query time
	reqTrack.dnsQueryTime = float64(dnsDone.Sub(dnsStart) / time.Microsecond)

	// TCP connection time
	reqTrack.tcpConnectTime = float64(connectDone.Sub(connectStart) / time.Microsecond)

	// TLS Handshake time
	reqTrack.tlsHandshakeTime = float64(tlsHandshakeDone.Sub(tlsHandshakeStart) / time.Microsecond)

	// Server processing time
	reqTrack.serverProcessingTime = float64(gotFirstResponseByte.Sub(gotConn) / time.Microsecond)

	// Content transfer
	reqTrack.contentTransferTime = float64(readBodyDone.Sub(gotFirstResponseByte) / time.Microsecond)

	// Total time to name lookup
	reqTrack.nameLookupTime = float64(dnsDone.Sub(dnsStart) / time.Microsecond)

	// Total time to connect
	reqTrack.connectTime = float64(connectDone.Sub(dnsStart) / time.Microsecond)

	// Total time to pretransfer
	reqTrack.pretransferTime = float64(gotConn.Sub(dnsStart) / time.Microsecond)

	// Total time to start transfer
	reqTrack.startTransferTime = float64(gotFirstResponseByte.Sub(dnsStart) / time.Microsecond)

	// Total time from start to finish reading body
	reqTrack.totalTime = float64(readBodyDone.Sub(dnsStart) / time.Microsecond)

	reqTrack.rps = reqData.RPS
	reqTrack.statusCode = resp.StatusCode

	mu.Lock()
	defer mu.Unlock()
	*reqsTracker = append(*reqsTracker, reqTrack)

	return nil

}

// requestHeadless loads the target URL with a shared headless browser and records the navigation time.
func requestHeadless(browser *rod.Browser, url *string, done chan bool) {
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

// sendRequestsHeadless schedules headless browser visits until the shared maxRequests limit is reached.
// Each worker reuses the shared browser instance but relies on reqCounter for coordination.
func sendRequestsHeadless(
	_ctx context.Context,
	browser *rod.Browser,
	url *string,
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
		go requestHeadless(browser, url, done)
		<-done
	}
}

// RequestData captures the parameters needed to build an HTTP request during benchmarking.
type RequestData struct {
	URL      string
	Method   string
	PostFile string
	RPS      int
	Cookies  []http.Cookie
}

// ChannelCounter serializes access to a counter via a dedicated goroutine.
type ChannelCounter struct {
	ch     chan func()
	number uint64
}

// NewCounterChannel creates a ChannelCounter initialized with the provided starting value.
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

// Add increases the counter by the supplied amount.
func (c *ChannelCounter) Add(num uint64) {
	c.ch <- func() {
		c.number = c.number + num
	}
}

// Read returns the current counter value.
func (c *ChannelCounter) Read() uint64 {
	ret := make(chan uint64)
	c.ch <- func() {
		ret <- c.number
		close(ret)
	}
	return <-ret
}

// RequestTracker stores timing metrics captured for a single HTTP request.
type RequestTracker struct {
	ch                   chan func()
	dnsQueryTime         float64
	tcpConnectTime       float64
	tlsHandshakeTime     float64
	serverProcessingTime float64
	contentTransferTime  float64
	nameLookupTime       float64
	connectTime          float64
	pretransferTime      float64
	startTransferTime    float64
	totalTime            float64
	rps                  int
	statusCode           int
}

// NewRequestTracker returns an empty slice ready to collect request metrics.
func NewRequestTracker() *[]RequestTracker {
	req := &[]RequestTracker{}
	return req
}

// AverageConnectTime prints and returns the average connect time across all tracked requests.
func AverageConnectTime(r *[]RequestTracker) float64 {
	sum := SumConnectTime(r)
	average := sum / float64(len(*r))

	averageFormatted, unit := formatDurationUnit(average)
	fmt.Printf("Average connect time\t\t%4.0f %s\n", averageFormatted, unit)

	return average
}

// AverageTotalTime prints and returns the average total time across all tracked requests.
func AverageTotalTime(r *[]RequestTracker) float64 {
	sum := SumTotalTime(r)
	average := sum / float64(len(*r))

	averageFormatted, unit := formatDurationUnit(average)
	fmt.Printf("Average time per request\t%4.0f %s\n", averageFormatted, unit)

	return average
}

// SumConnectTime sums the connect times (in microseconds) for every tracked request.
func SumConnectTime(r *[]RequestTracker) float64 {
	sum := 0.0
	for _, reqTrack := range *r {
		sum += reqTrack.connectTime
	}
	return sum
}

// SumTotalTime sums the total times (in microseconds) for every tracked request.
func SumTotalTime(r *[]RequestTracker) float64 {
	sum := 0.0
	for _, reqTrack := range *r {
		sum += reqTrack.totalTime
	}
	return sum
}

// RequestPerSecond calculates and prints the throughput using the recorded timings and declared request count.
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

// GenerateOutput writes raw timing data to output.csv for later visualization.
func GenerateOutput(r *[]RequestTracker, maxRequests uint64) {
	file, err := os.Create("output.csv")
	if err != nil {
		log.Printf("failed to create output.csv: %v", err)
		return
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	if _, err := writer.WriteString("Name Lookup;Connect;Pretransfer;Start Transfer;Reading Body;Status\n"); err != nil {
		log.Printf("failed to write header: %v", err)
		return
	}

	for _, reqTrack := range *r {
		if _, err := fmt.Fprintf(writer, "%v;%v;%v;%v;%v;%v\n",
			reqTrack.nameLookupTime/1_000_000, reqTrack.connectTime/1_000_000,
			reqTrack.pretransferTime/1_000_000, reqTrack.startTransferTime/1_000_000,
			reqTrack.totalTime/1_000_000, reqTrack.statusCode,
		); err != nil {
			log.Printf("failed to write row: %v", err)
			return
		}
	}

	if err := writer.Flush(); err != nil {
		log.Printf("failed to flush output.csv: %v", err)
	}

	fmt.Printf("Generating results to: output.csv\n")

}

// readFile returns the contents of the provided path, validating that it is non-empty and readable.
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

// formatDurationUnit scales a microsecond duration into a human-readable value and accompanying unit label.
func formatDurationUnit(t float64) (float64, string) {
	if t > 1000 && t < 1000000 {
		return t / 1000, "ms"
	}
	if t >= 1000000 {
		return t / 1000000, "s"
	}
	return t, "μs"
}

// testConnection probes the target endpoint with a HEAD request to surface connectivity or TLS issues early.
func testConnection() error {
	// Test a connection and check if it is successful
	client := NewHTTPClient(*skipTLS, *E)
	err := request(client, RequestData{URL: *url, Method: "HEAD"})
	if err != nil && strings.Contains(err.Error(), "tls: failed to verify certificate") {
		fmt.Fprintf(os.Stderr, "TLS error occurred. If you want to skip TLS verification, use -skipTLS flag\n")
		return err
	}

	return nil
}
