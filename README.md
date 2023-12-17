# fire-ant
Simple server benchmark


### Compile
You need to install Go 1.20 or more..

```bash
go build main.go
```

### How to use

```
# This will send total 100 requests, but will be sent concurrently using 10 workers (can be seen as client):
./main -n 100 -c 10

# If you want to send request sequentially (which will not reflect the real performance), use this:
./main -n 100 -c 1
```