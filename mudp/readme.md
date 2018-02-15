MUDP
-------
MUDP implements a udp network server and client library for the `mnet` package to provide superfast reads and writes.

## Benchmarks

Below are recently runned benchmarks, see [BenchmarkTests](./benchmark.txt) for older runs.

```bash
goos: darwin
goarch: amd64
pkg: github.com/influx6/mnet/mudp
BenchmarkNoBytesMessages-4    	 3000000	       433 ns/op	  13.85 MB/s	      16 B/op	       1 allocs/op
Benchmark2BytesMessages-4     	 3000000	       435 ns/op	  18.38 MB/s	      16 B/op	       1 allocs/op
Benchmark4BytesMessages-4     	 3000000	       442 ns/op	  22.59 MB/s	      16 B/op	       1 allocs/op
Benchmark8BytesMessages-4     	 3000000	       450 ns/op	  31.05 MB/s	      16 B/op	       1 allocs/op
Benchmark16BytesMessages-4    	 3000000	       429 ns/op	  51.22 MB/s	      16 B/op	       1 allocs/op
Benchmark32BytesMessages-4    	 3000000	       446 ns/op	  85.15 MB/s	      16 B/op	       1 allocs/op
Benchmark64BytesMessages-4    	 3000000	       448 ns/op	 156.16 MB/s	      16 B/op	       1 allocs/op
Benchmark128BytesMessages-4   	 3000000	       445 ns/op	 300.74 MB/s	      16 B/op	       1 allocs/op
Benchmark256BytesMessages-4   	 3000000	       435 ns/op	 601.86 MB/s	      16 B/op	       1 allocs/op
Benchmark1KMessages-4         	 3000000	       439 ns/op	2345.05 MB/s	      16 B/op	       1 allocs/op
Benchmark4KMessages-4         	 3000000	       455 ns/op	9001.28 MB/s	      16 B/op	       1 allocs/op
Benchmark8KMessages-4         	 3000000	       466 ns/op	17580.83 MB/s	      16 B/op	       1 allocs/op
Benchmark16KMessages-4        	 3000000	       460 ns/op	35576.56 MB/s	      16 B/op	       1 allocs/op
PASS
ok  	github.com/influx6/mnet/mudp	23.583
```

## Examples

#### MUDP Server
`mudp.Network` provides a udp network server which readily through a handler function allows handling incoming client connections, as below: 

```go
var netw mudp.Network
netw.Network = "udp"
netw.ReadBuffer = 0
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

        writer.Write("welcome")
        writer.Flush()
    }
}

```

#### Client

`mudp.Connect` provides a udp client which connects to a `mudp.Network` server, ready to allow user's communication at blazing speeds.

```go
client, err := mudp.Connect("localhost:4050")
if err != nil {
    log.Fatalf(err)
    return
}

// create writer by telling client size of data
// to be written.
writer, err := client.Write(10)
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

## A note on UDP/IP OS Buffer sizes

Some OSes (most notably, Linux) place very restricive limits on the performance
of UDP protocols. It is _highly_ recommended that you increase these OS limits to
at least 25MB before trying to run UDP traffic to your instance.
25MB is just a recommendation, and should be adjusted to be inline with your
`read-buffer` plugin setting.

### Linux
Check the current UDP/IP receive buffer default and limit by typing the following commands:

```
sysctl net.core.rmem_max
sysctl net.core.rmem_default
```

If the values are less than 26214400 bytes (25MB) you should add the following lines to the /etc/sysctl.conf file:

```
net.core.rmem_max=26214400
net.core.rmem_default=26214400
```

Changes to /etc/sysctl.conf do not take effect until reboot.  To update the values immediately, type the following commands as root:

```
sysctl -w net.core.rmem_max=26214400
sysctl -w net.core.rmem_default=26214400
```

### BSD/Darwin

On BSD/Darwin systems you need to add about a 15% padding to the kernel limit
socket buffer. Meaning if you want a 25MB buffer (26214400 bytes) you need to set
the kernel limit to `26214400*1.15 = 30146560`. This is not documented anywhere but
happens
[in the kernel here.](https://github.com/freebsd/freebsd/blob/master/sys/kern/uipc_sockbuf.c#L63-L64)

Check the current UDP/IP buffer limit by typing the following command:

```
sysctl kern.ipc.maxsockbuf
```

If the value is less than 30146560 bytes you should add the following lines to the /etc/sysctl.conf file (create it if necessary):

```
kern.ipc.maxsockbuf=30146560
```

Changes to /etc/sysctl.conf do not take effect until reboot.  To update the values immediately, type the following commands as root:

```
sysctl -w kern.ipc.maxsockbuf=30146560
```


## Setting ReadBuffer on `mudp.Network`

Simply set the `mudp.Network.ReadBuffer` to value set on the os through any of the os platform specific commands detailed in the previous sections, else set to `0` to indicate that it should use the default set by the OS.