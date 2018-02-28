package mtcp_test

import (
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"net"
	"testing"
	"time"

	"context"

	"github.com/influx6/mnet"
	"github.com/influx6/mnet/mtcp"
)

var (
	defaultClientSize = 32768
)

func BenchmarkNoBytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(0)
	benchThis(b, payload)
}

func Benchmark2BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(2)
	benchThis(b, payload)
}

func Benchmark4BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(4)
	benchThis(b, payload)
}

func Benchmark8BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(8)
	benchThis(b, payload)
}

func Benchmark16BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(16)
	benchThis(b, payload)
}

func Benchmark32BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(32)
	benchThis(b, payload)
}

func Benchmark64BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(64)
	benchThis(b, payload)
}

func Benchmark128BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(128)
	benchThis(b, payload)
}

func Benchmark256BytesMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(256)
	benchThis(b, payload)
}

func Benchmark1KMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(1024)
	benchThis(b, payload)
}

func Benchmark4KMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(4 * 1024)
	benchThis(b, payload)
}

func Benchmark8KMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(8 * 1024)
	benchThis(b, payload)
}

func Benchmark16KMessages(b *testing.B) {
	b.StopTimer()
	payload := sizedPayload(16 * 1024)
	benchThis(b, payload)
}

func benchThis(b *testing.B, payload []byte) {
	b.StopTimer()
	b.ReportAllocs()

	chosenAddr := fmt.Sprintf("localhost:%d", freePort())

	payloadLen := len(payload)
	ctx, cancel := context.WithCancel(context.Background())
	netw, err := createBenchmarkNetwork(ctx, chosenAddr, nil, defaultClientSize)
	if err != nil {
		b.Fatalf("Failed to create network: %+q", err)
		return
	}

	netw.MaxWriteSize = defaultClientSize

	client, err := mtcp.Connect(chosenAddr, mtcp.MaxBuffer(defaultClientSize))
	if err != nil {
		b.Fatalf("Failed to dial network %+q", err)
		return
	}

	b.SetBytes(int64(payloadLen))
	b.StartTimer()

	for i := 0; i < b.N; i++ {
		if w, err := client.Write(payloadLen); err == nil {
			w.Write(payload)
			w.Close()
		}
	}

	client.Flush()

	b.StopTimer()
	client.Close()
	cancel()
	netw.Wait()
}

func createBenchmarkNetwork(ctx context.Context, addr string, config *tls.Config, size int) (*mtcp.TCPNetwork, error) {
	var netw mtcp.TCPNetwork
	netw.Addr = addr
	netw.TLS = config
	netw.MaxWriteSize = size

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
		}
	}

	return &netw, netw.Start(ctx)
}

var pub = []byte("pub ")
var ch = []byte("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@$#%^&*()")

func sizedPayloadString(sz int) string {
	return string(sizedPayload(sz))
}

func sizedPayload(sz int) []byte {
	payload := make([]byte, len(pub)+2+sz)
	n := copy(payload, pub)
	n += copy(payload[n:], sizedBytes(sz))
	n += copy(payload, []byte("\r\n"))
	return payload[:n]
}

func sizedBytes(sz int) []byte {
	if sz <= 0 {
		return []byte("")
	}

	b := make([]byte, sz)
	for i := range b {
		b[i] = ch[rand.Intn(len(ch))]
	}
	return b
}

func sizedString(sz int) string {
	return string(sizedBytes(sz))
}

type writtenBuffer struct {
	c            int
	totalWritten int
}

func (b *writtenBuffer) Write(d []byte) (int, error) {
	b.c += 1
	b.totalWritten += len(d)
	return len(d), nil
}

func freePort() int {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		log.Fatal(err)
		return 0
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		log.Fatal(err)
		return 0
	}

	defer l.Close()

	return l.Addr().(*net.TCPAddr).Port
}
