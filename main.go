package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"sync"
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
	workerCounter := NewCounterChannel(0)

	// Monitor each routine and wait them until desired number of requests is reached
	workers := sync.WaitGroup{}
	for i := int64(0); i < *c; i++ {
		workers.Add(1)

		// Unique http client will be used and reused for 1 routine
		client := &http.Client{}

		// This go routine will start sending requests sequentially one after each request is completed
		go sendRequests(ctx, client, url, i, workerCounter, &workers)
	}
	workers.Wait()
}

// Tell the client to send request sequentially until maxRequests is reached
// Each client will not depend on each other and has its own request timeline.
func sendRequests(_ctx context.Context, client *http.Client, url *string, workerNumber int64, connCounter *ChannelCounter, wg *sync.WaitGroup) {
	// done is to indicate that a request has got response
	done := make(chan bool)
	defer wg.Done()

	for {
		nRequests := connCounter.Read()
		if nRequests > maxRequests {
			break
		}
		connCounter.Add(1)

		// We can send request synchronously, but we will use go routine for further operation
		go request(client, url, done)
		<-done
	}

}

// Send a http request to specified url using a specified client
func request(client *http.Client, url *string, done chan bool) {
	req, err := http.NewRequest("GET", *url, nil)

	if err != nil {
		fmt.Println(err)
		done <- true
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		done <- true
		return
	}
	_ = resp

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
