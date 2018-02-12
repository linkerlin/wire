package internal_test

import (
	"testing"

	"io"

	"bytes"

	"time"

	"github.com/influx6/faux/pools/done"
	"github.com/influx6/faux/tests"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/internal"
)

func BenchmarkStreamedMessages(b *testing.B) {
	b.StopTimer()
	b.ReportAllocs()

	streamId := "32-woff"
	streams := internal.NewStreamedMessages(time.Minute*1, false)

	ch := new(doWriter)
	ch.do = func(p []byte) error {
		return streams.Parse(p)
	}

	c := mnet.Client{
		WriteFunc: func(_ mnet.Client, i int) (io.WriteCloser, error) {
			return &limitWriter{limit: i, chunks: ch}, nil
		},
	}

	toW := toWriter{data: data}

	b.SetBytes(int64(len(data)))
	b.StartTimer()
	for i := 0; i < b.N; i++ {
		newStream := internal.NewStreamWriter(c, streamId, 128, 10)
		toW.WriteTo(newStream)
		newStream.Close()
	}
	b.StopTimer()
}

func TestStreamedMessages(t *testing.T) {
	streams := internal.NewStreamedMessages(time.Minute*1, false)

	ch := new(doWriter)
	ch.do = func(p []byte) error {
		return streams.Parse(p)
	}

	var c mnet.Client
	c.WriteFunc = func(_ mnet.Client, i int) (io.WriteCloser, error) {
		return &limitWriter{limit: i, chunks: ch}, nil
	}

	newStream := internal.NewStreamWriter(c, "32-woff", 128, 10)

	bu := bytes.NewBuffer(data)
	if _, err := bu.WriteTo(newStream); err != nil {
		tests.FailedWithError(err, "Should have successfully written data to stream")
	}
	tests.Passed("Should have successfully written data to stream")

	if err := newStream.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully closed stream")
	}
	tests.Passed("Should have successfully closed stream")

	rec, meta, err := streams.Next()
	if err != nil {
		tests.FailedWithError(err, "Should have successfully collated streamed data")
	}
	tests.Passed("Should have successfully collated streamed data")

	if !bytes.Equal(data, rec) {
		tests.Info("Received: %+q\n", rec)
		tests.Info("Expected: %+q\n", data)
		tests.Failed("Should have matched received with input")
	}
	tests.Passed("Should have matched received with input")

	if meta.Total != 128 {
		tests.Failed("Should have matched total data sent")
	}
	tests.Passed("Should have matched total data sent")
}

func TestClientStream(t *testing.T) {
	ch := new(chunkWriter)

	var c mnet.Client
	c.WriteFunc = func(_ mnet.Client, i int) (io.WriteCloser, error) {
		return &limitWriter{limit: i, chunks: ch}, nil
	}

	newStream := internal.NewStreamWriter(c, "32-woff", 128, 10)

	bu := bytes.NewBuffer(data)
	if _, err := bu.WriteTo(newStream); err != nil {
		tests.FailedWithError(err, "Should have successfully written data to stream")
	}
	tests.Passed("Should have successfully written data to stream")

	if err := newStream.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully closed stream")
	}
	tests.Passed("Should have successfully closed stream")

	for index, elem := range ch.chunks {
		other := expected[index]
		if !bytes.Equal(other, elem) {
			tests.Failed("Should have matched chunked data at index %d", index)
		}
	}

	tests.Passed("Should have matched all streamed data")
}

type toWriter struct {
	data []byte
}

func (t toWriter) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(t.data)
	return int64(n), err
}

type limitWriter struct {
	chunks io.Writer
	cc     []byte
	limit  int
	c      int
}

func (l *limitWriter) Close() error {
	_, err := l.chunks.Write(l.cc)
	return err
}

func (l *limitWriter) Write(p []byte) (int, error) {
	l.c += len(p)
	if l.c > l.limit {
		return len(p), done.ErrLimitExceeded
	}

	l.cc = append(l.cc, p...)
	return len(p), nil
}

type doWriter struct {
	do func([]byte) error
}

func (l *doWriter) Close() error {
	return nil
}

func (l *doWriter) Write(p []byte) (int, error) {
	return len(p), l.do(p)
}

type chunkWriter struct {
	chunks [][]byte
}

func (l *chunkWriter) Close() error {
	return nil
}

func (l *chunkWriter) Write(p []byte) (int, error) {
	l.chunks = append(l.chunks, p)
	return len(p), nil
}

var (
	data = []byte{0xec, 0xe6, 0x2d, 0x1a, 0xbe, 0xd, 0xc0, 0x14, 0x88, 0xc4, 0xf9, 0x39, 0xc9, 0xbc, 0xf0, 0xf1, 0x1e, 0x62, 0xe9, 0x80, 0x67, 0x43, 0x7, 0x1a, 0xde, 0x7d, 0x39, 0x45, 0x51, 0xe1, 0xb7, 0xef, 0xbd, 0xf6, 0xc3, 0xfb, 0x18, 0xf7, 0xf7, 0x7c, 0xe, 0x81, 0x40, 0x13, 0x1c, 0xd7, 0x23, 0x6, 0x4e, 0x7c, 0x45, 0xe0, 0x1a, 0xda, 0x8d, 0xf6, 0x9e, 0x68, 0x6a, 0xd7, 0xbd, 0x6a, 0x21, 0xdd, 0x4e, 0x58, 0xb6, 0xc6, 0x32, 0xa2, 0x14, 0x13, 0xb2, 0x36, 0xd9, 0xe0, 0x9f, 0x1b, 0x8c, 0x85, 0x83, 0xde, 0x8, 0xe3, 0xd1, 0x14, 0x4b, 0x98, 0x4c, 0xcc, 0xe5, 0xd, 0x3, 0x73, 0x44, 0x14, 0xf9, 0x72, 0x78, 0xf, 0x23, 0xbd, 0xfd, 0x0, 0x1e, 0x36, 0xc0, 0xeb, 0xe, 0xb2, 0xa5, 0xb2, 0xd9, 0x76, 0xe8, 0xa8, 0x41, 0x51, 0x94, 0xe4, 0x6c, 0x76, 0x20, 0x7d, 0x70, 0x52, 0xab, 0xbc}

	expected = [][]uint8{[]uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x30, 0x3a, 0x31, 0x30, 0xd, 0xa, 0xec, 0xe6, 0x2d, 0x1a, 0xbe, 0xd, 0xc0, 0x14, 0x88, 0xc4}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x31, 0x30, 0x3a, 0x32, 0x30, 0xd, 0xa, 0xf9, 0x39, 0xc9, 0xbc, 0xf0, 0xf1, 0x1e, 0x62, 0xe9, 0x80}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x32, 0x30, 0x3a, 0x33, 0x30, 0xd, 0xa, 0x67, 0x43, 0x7, 0x1a, 0xde, 0x7d, 0x39, 0x45, 0x51, 0xe1}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x33, 0x30, 0x3a, 0x34, 0x30, 0xd, 0xa, 0xb7, 0xef, 0xbd, 0xf6, 0xc3, 0xfb, 0x18, 0xf7, 0xf7, 0x7c}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x34, 0x30, 0x3a, 0x35, 0x30, 0xd, 0xa, 0xe, 0x81, 0x40, 0x13, 0x1c, 0xd7, 0x23, 0x6, 0x4e, 0x7c}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x35, 0x30, 0x3a, 0x36, 0x30, 0xd, 0xa, 0x45, 0xe0, 0x1a, 0xda, 0x8d, 0xf6, 0x9e, 0x68, 0x6a, 0xd7}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x36, 0x30, 0x3a, 0x37, 0x30, 0xd, 0xa, 0xbd, 0x6a, 0x21, 0xdd, 0x4e, 0x58, 0xb6, 0xc6, 0x32, 0xa2}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x37, 0x30, 0x3a, 0x38, 0x30, 0xd, 0xa, 0x14, 0x13, 0xb2, 0x36, 0xd9, 0xe0, 0x9f, 0x1b, 0x8c, 0x85}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x38, 0x30, 0x3a, 0x39, 0x30, 0xd, 0xa, 0x83, 0xde, 0x8, 0xe3, 0xd1, 0x14, 0x4b, 0x98, 0x4c, 0xcc}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x39, 0x30, 0x3a, 0x31, 0x30, 0x30, 0xd, 0xa, 0xe5, 0xd, 0x3, 0x73, 0x44, 0x14, 0xf9, 0x72, 0x78, 0xf}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x31, 0x30, 0x30, 0x3a, 0x31, 0x31, 0x30, 0xd, 0xa, 0x23, 0xbd, 0xfd, 0x0, 0x1e, 0x36, 0xc0, 0xeb, 0xe, 0xb2}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x31, 0x31, 0x30, 0x3a, 0x31, 0x32, 0x30, 0xd, 0xa, 0xa5, 0xb2, 0xd9, 0x76, 0xe8, 0xa8, 0x41, 0x51, 0x94, 0xe4}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x30, 0x3a, 0x31, 0x32, 0x30, 0x3a, 0x31, 0x33, 0x30, 0xd, 0xa, 0x6c, 0x76, 0x20, 0x7d, 0x70, 0x52, 0xab, 0xbc}, []uint8{0x53, 0x54, 0x52, 0x4d, 0x45, 0x3a, 0x33, 0x32, 0x2d, 0x77, 0x6f, 0x66, 0x66, 0x3a, 0x31, 0x32, 0x38, 0x3a, 0x31, 0x32, 0x38, 0xd, 0xa}}
)
