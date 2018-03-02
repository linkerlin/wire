MTCP
-------
MTCP implements a tcp network server and client library for the `mnet` package to provide superfast reads and writes. Mtcp guarantees double digits nanoseconds transfer regardless of data size.


## Benchmarks

Below are recently runned benchmarks, see [BenchmarkTests](./benchmark.txt) for older runs.

```bash
oos: darwin
goarch: amd64
pkg: github.com/wirekit/wire/mtcp
BenchmarkNoBytesMessages-4    	50000000	        28.6 ns/op	 244.89 MB/s	       0 B/op	       0 allocs/op
Benchmark2BytesMessages-4     	50000000	        28.6 ns/op	 314.14 MB/s	       0 B/op	       0 allocs/op
Benchmark4BytesMessages-4     	50000000	        28.0 ns/op	 392.20 MB/s	       0 B/op	       0 allocs/op
Benchmark8BytesMessages-4     	50000000	        28.7 ns/op	 523.40 MB/s	       0 B/op	       0 allocs/op
Benchmark16BytesMessages-4    	50000000	        29.3 ns/op	 785.38 MB/s	       0 B/op	       0 allocs/op
Benchmark32BytesMessages-4    	50000000	        29.0 ns/op	1342.68 MB/s	       0 B/op	       0 allocs/op
Benchmark64BytesMessages-4    	50000000	        28.2 ns/op	2513.39 MB/s	       0 B/op	       0 allocs/op
Benchmark128BytesMessages-4   	50000000	        28.4 ns/op	4753.42 MB/s	       0 B/op	       0 allocs/op
Benchmark256BytesMessages-4   	50000000	        28.7 ns/op	9173.86 MB/s	       0 B/op	       0 allocs/op
Benchmark1KMessages-4         	50000000	        28.1 ns/op	36674.89 MB/s	       0 B/op	       0 allocs/op
Benchmark4KMessages-4         	50000000	        28.7 ns/op	142932.97 MB/s	       0 B/op	       0 allocs/op
Benchmark8KMessages-4         	50000000	        28.5 ns/op	287551.97 MB/s	       0 B/op	       0 allocs/op
Benchmark16KMessages-4        	50000000	        28.4 ns/op	577755.46 MB/s	       0 B/op	       0 allocs/op
PASS
ok  	github.com/wirekit/wire/mtcp	19.931s
```

## Examples

- MTCP Server
`mtcp.Network` provides a tcp network server which readily through a handler function allows handling incoming client connections, as below: 

```go
var netw mtcp.Network
netw.TLS = config
netw.Addr = "localhost:5050"
netw.Handler = func(client mnet.Client) error {
    // Flush all incoming data out
    for {
        _, err := client.Read()
        if err != nil {
            if err == mnet.ErrNoDataYet {
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
    if readErr != nil && readErr == mnet.ErrNoDataYet {
        continue
    }

    // do stuff with data
}
```