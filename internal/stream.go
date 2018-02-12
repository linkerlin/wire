package internal

import (
	"io"
	"sync"
	"sync/atomic"

	"strconv"

	"github.com/influx6/faux/pools/done"
	"github.com/influx6/mnet"
)

//*************************************************************************
// StreamReader Implementation
//*************************************************************************

//// StreamReader implements mnet.StreamReader which wraps an existing
//// mnet.Client instance and handles the necessary logic to piece together
//// chunked streamed contents that were transferred over the wire.
//type StreamReader struct {
//	mnet.Client
//	collator *StreamedMessages
//}
//
//// NewStreamReader returns a new instance of a StreamReader.
//func NewStreamReader(c mnet.Client, maxStaleness time.Duration, keepFailed bool) *StreamReader {
//	return &StreamReader{
//		Client:   c,
//		collator: NewStreamedMessages(maxStaleness, keepFailed),
//	}
//}
//
//// Failed returns all failed and uncompleted/staled streams
//// which may allow you request missing areas as desired.
//func (sr *StreamReader) Failed() map[string]*Streamed {
//	return sr.collator.FailedStreams()
//}
//
//// Read attempts to read data from it's composed mnet.Client, if non
//// is received, it then queries it's internal stream collector/collator
//// which if it has any completed streams, will return a completed
//// stream data and meta information. If the client returns new data which
//// it itself is not a stream part/chunk, then that is returned as is with no
//// associated meta.
//func (sr *StreamReader) Read() ([]byte, *mnet.Meta, error) {
//
//}

//*************************************************************************
// StreamWriter Implementation
//*************************************************************************

// const of stream and stream end headers and length.
const (
	STRMHeader    = "STRM"
	STRMEndHeader = "STRME"
)

// StreamWriter implements the io.WriteCloser for the stream writing of large data
// into small sizable chunks. Each message is tagged with a defined consistent id.
// It is heavily dependent on the capability of the the writer from the client to
// hold data, it will collect enough data as it desired which is chunked within the
// desired chunk size. If the writer returns a internal.ErrLimitExceeded, then
// the writer is closed and a new one is requested, this way, we are able to send
// as much data as is allowed by the writer implementation but still chunk within
// desired size.
//
// WARNING: The collected total will not be allowed to exceed the total indicated
// to the stream writer, because it simplifies alot of complicated calculations on
// both ends, so we would advice the user to strive to attain total data of any writer
// accurately.
type StreamWriter struct {
	c           mnet.Client
	beginFormat string
	endFormat   string
	streamId    string
	chunk       int
	total       int
	live        int64
	offset      int

	cm sync.Mutex
	cw io.WriteCloser
}

// NewStreamWriter returns a new instance of StreamWriter type for the provided Client
// with the associated chunk size.
func NewStreamWriter(c mnet.Client, streamId string, total int, chunk int) *StreamWriter {
	ws := &StreamWriter{
		c:        c,
		chunk:    chunk,
		total:    total,
		streamId: streamId,
	}

	totalString := strconv.Itoa(total)
	chunkString := strconv.Itoa(chunk)
	ws.endFormat = STRMEndHeader + string(colon) + streamId + string(colon) + totalString
	ws.beginFormat = STRMHeader + string(colon) + streamId + string(colon) + totalString + string(colon) + chunkString
	return ws
}

// Close ends all streaming with giving ClientStream, where any calls to the
// StreamWriter.Write will return an error.
// This sends a final data of format:
//
// STRME:STREAM_ID:STREAM_TOTAL:LASTOFFSET\r\n
//
// It then closes writer and internal logic.
func (c *StreamWriter) Close() error {
	if atomic.LoadInt64(&c.live) == 1 {
		return mnet.ErrAlreadyClosed
	}

	atomic.StoreInt64(&c.live, 1)

	offsetString := strconv.Itoa(c.offset)
	header := c.endFormat + string(colon) + offsetString + string(ctrl)
	headerSlice := []byte(header)

	var cw io.WriteCloser
	var err error

	// if we have no writer, make one else use existing one.
	c.cm.Lock()
	if c.cw == nil {
		c.cw, err = c.c.Write(len(header))
		cw = c.cw
	} else {
		cw = c.cw
		c.cw = nil
	}
	c.cm.Unlock()

	// if we failed to get a new writer then return err.
	if err != nil {
		return err
	}

	// Attempt to write ending header, if we fail, check if
	// its a LimitExceeded error, creating a new writer to handle
	// the header.
	_, err = cw.Write(headerSlice)
	if err != nil {
		if err == done.ErrLimitExceeded || err == ErrLimitExceeded {
			cw.Close()

			c.cm.Lock()
			cw, err = c.c.Write(len(header))
			c.cm.Unlock()

			if err != nil {
				return err
			}

			_, err = cw.Write(headerSlice)
			cw.Close()
			return err
		}

		return err
	}

	return nil
}

// Write writes incoming data into stream with tagged id associated
// with data.
// Write does a few tricks underneath, every data that is received is
// chunked into pieces of chunk size as provided to the StreamWriter, but
// the writer received from the client is allowed to accumulate as much as
// it can hold till we get a internal.ErrLimitExceeded, which then closes the
// current writer. If more data is still pending writing, then a new writer is
// requested from the client to service the rest of data or new incoming ones.
//
// WARNING: The collected total will not be allowed to exceed the total indicated
// to the stream writer, because it simplifies alot of complicated calculations on
// both ends, so we would advice the user to strive to attain total data of any writer
// accurately.
func (c *StreamWriter) Write(p []byte) (int, error) {
	if atomic.LoadInt64(&c.live) == 1 {
		return 0, mnet.ErrStreamerClosed
	}

	if c.offset >= c.total {
		return 0, ErrTotalExceeded
	}

	var n int
	var err error
	var cw io.WriteCloser

	c.cm.Lock()
	if c.cw == nil {
		n, err = c.newWrite()
	}
	cw = c.cw
	c.cm.Unlock()

	if err != nil {
		return n, err
	}

	var target, rest []byte
	if len(p) <= c.chunk {
		target = p
	} else {
		target = p[:c.chunk]
		rest = p[c.chunk:]
	}

	n, err = c.cw.Write(target)
	if err != nil {
		if err == io.ErrShortWrite {
			err = nil
			rest = p[n:]
			c.offset += n
			return c.Write(rest)
		}

		if err == done.ErrLimitExceeded || err == ErrLimitExceeded {
			if err := cw.Close(); err != nil {
				return n, err
			}

			c.cm.Lock()
			c.cw = nil
			c.cm.Unlock()
			return c.Write(p)
		}

		return c.offset, err
	}

	c.offset += n
	if len(rest) != 0 {
		return c.Write(rest)
	}

	return c.offset, nil
}

// newWriter creates a new writer from the client for the giving chunk size plus
// associated length of Stream headers added. It will always send the next chunk in
// format:
//
// STRM:STREAM_ID:STREAM_TOTAL:CHUNK_SIZE:LASTOFFSET:NEXTOFFSET\r\nCHUNKBYTES
//
// This allows the receiver to identify each chunk stream easily.
// It sets the new writer as the current writer.
func (c *StreamWriter) newWrite() (int, error) {
	offsetString := strconv.Itoa(c.offset)
	endOffsetString := strconv.Itoa(c.offset + c.chunk)
	header := c.beginFormat + string(colon) + offsetString + string(colon) + endOffsetString + string(ctrl)

	chunkedLen := c.chunk + len(header)
	newWriter, err := c.c.Write(chunkedLen)
	if err != nil {
		return 0, err
	}

	c.cw = newWriter

	return newWriter.Write([]byte(header))
}
