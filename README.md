# Sumba
Simple server benchmark tool.

### Origin
Sumba is named after the stunning island in East Nusa Tenggara, Indonesia,
known for its pristine beaches, vibrant culture, and untamed natural beauty.
We chose this name to reflect our app's essence: a simple, yet powerful server
benchmark tool that delivers clear, reliable performance insights with the
elegance and efficiency inspired by Sumba’s raw charm.



### Compile
You need to install Go 1.20 or the latest one.

```bash
go build -o sumba main.go
```

Or you can run without compiling.
```bash
go run main.go
```

### Download
If you prefer pre-compiled binary, you can download it in [Releases page](https://github.com/dukenmarga/sumba/releases). Supported version:
- Linux x86_64
- Macos Arm64
- Windows x86_64
- FreeBSD x86_64

### How to use

* This will send total 100 requests, but will be sent concurrently using 10 workers (can be seen as client):
    ```bash
    $ ./sumba -n 100 -c 10 -url http://localhost
    ```

* If you want to send request sequentially (which will not reflect the real performance), use this:
    ```bash
    $ ./sumba -n 100 -c 1 -url http://localhost
    ```

* If you want to emulate to test a URL using headless browser (open a page and download all its resources such as CSS, JS, etc), use this:
    ```bash
    $ ./sumba -n 100 -c 1 -url http://localhost -emulate true
    ```


* Example with the results
    ```bash
    ./sumba -n 100 -c 3 -url https://google.com
    Test 100 requests using 3 workers to: https://google.com
    Average first byte time         78.8 ms
    Average time per request        78.9 ms
    Request per second                13 req/s
    ```

### Options

```
-C "key1=value1; key2=value2"
    Add cookie to the request in the form of "key1=value1; key2=value2"
```

### How it works
The app will send requests to target URL until it reaches value defined by parameter `n`
and use `c` number of workers (clients) to finish all requests.

Let's say `n = 10` and `c = 3`. We can create simulation diagram below using 3 clients (workers)
and request time for each client. All 3 clients will start sending request in parallel (#1 - #3). Everytime a request was completed, the client will send another request.
The last request (#10) will be handled by client 3 and client 1 and 2 will not create any
request since total 10 requests have been reached.

```
client 1: ####(1) -> #####(4) -> #####(8)
client 2: ######(2) -> #####(6) -> #####(9)
client 3: #####(3) -> ###(5) -> ####(7) -> ####(10)
```

Total request time (e.g 10 requests above) will be used to determine the server performance.

```
total time = req (1) + req (2) + ... + req (n)

rps = total time / n
```