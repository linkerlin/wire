MTCP
-------
MTCP implements a tcp network server and client library for the `mnet` package to provide superfast reads and writes. Mtcp guarantees double digits nanoseconds transfer regardless of data size.


## Benchmarks

Below are recently runned benchmarks, see [BenchmarkTests](./benchmark.txt) for older runs.

```bash
goos: darwin
goarch: amd64
pkg: github.com/wirekit/wire/mtcp
BenchmarkNoBytesMessages-4    	100000000	        17.4 ns/op	 344.68 MB/s	       0 B/op	       0 allocs/op
Benchmark2BytesMessages-4     	100000000	        19.4 ns/op	 413.00 MB/s	       0 B/op	       0 allocs/op
Benchmark4BytesMessages-4     	100000000	        16.3 ns/op	 614.39 MB/s	       0 B/op	       0 allocs/op
Benchmark8BytesMessages-4     	100000000	        16.2 ns/op	 863.94 MB/s	       0 B/op	       0 allocs/op
Benchmark16BytesMessages-4    	100000000	        16.5 ns/op	1330.94 MB/s	       0 B/op	       0 allocs/op
Benchmark32BytesMessages-4    	100000000	        16.5 ns/op	2307.18 MB/s	       0 B/op	       0 allocs/op
Benchmark64BytesMessages-4    	100000000	        16.5 ns/op	4233.78 MB/s	       0 B/op	       0 allocs/op
Benchmark128BytesMessages-4   	100000000	        15.9 ns/op	8441.12 MB/s	       0 B/op	       0 allocs/op
Benchmark256BytesMessages-4   	100000000	        16.1 ns/op	16299.86 MB/s	       0 B/op	       0 allocs/op
Benchmark1KMessages-4         	100000000	        15.7 ns/op	65450.64 MB/s	       0 B/op	       0 allocs/op
Benchmark4KMessages-4         	100000000	        16.1 ns/op	254607.62 MB/s	       0 B/op	       0 allocs/op
Benchmark8KMessages-4         	100000000	        15.8 ns/op	517389.06 MB/s	       0 B/op	       0 allocs/op
Benchmark16KMessages-4        	100000000	        16.1 ns/op	1016121.55 MB/s	       0 B/op	       0 allocs/op
PASS
ok  	github.com/wirekit/wire/mtcp	26.730s
```

## Examples

- MTCP Server
`mtcp.Network` provides a tcp network server which readily through a handler function allows handling incoming client connections, as below: 

```go
var netw mtcp.Network
netw.TLS = config
netw.Addr = "localhost:5050"
netw.Handler = func(client wire.Client) error {
    // Flush all incoming data out
    for {
        _, err := client.Read()
        if err != nil {
            if err == wire.ErrNoDataYet {
                time.Sleep(300 * time.Millisecond)
                continue
            }

            return err
        }

		// Get writer with 5byte space for response.
		if writer, err := client.Write(7); err == nil {
			writer.Write("welcome")
			writer.Close()
		}
		
		client.Flush()
    }
}

```

- Client
`mtcp.Connect` provides a tcp client which connects to a `mtcp.Network` server, ready to allow user's communication at blazing speeds.

```go
client, err := mtcp.Connect("localhost:4050")
if err != nil {
    log.Fatalf(err)
    return
}

// create writer by telling client size of data
// to be written.
writer, err := client.Write(8)
if err != nil {
    log.Fatalf(err)
    return
}

writer.Write([]byte("pub help"))
if err := writer.Close(); err != nil {
    log.Fatalf(err)
    return
}

if _, err := client.Flush(); err != nil {
    log.Fatalf(err)
    return
}

for {
    res, readErr := client.Read()
    if readErr != nil && readErr == wire.ErrNoDataYet {
        continue
    }

    // do stuff with data
}
```