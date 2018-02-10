package internal

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/influx6/faux/pools/done"
	"github.com/influx6/mnet"
)

//*************************************************************************
// StreamWriter Implementation
//*************************************************************************

// const of stream and stream end headers and length.
const (
	STRMHeader       = "STRM"
	STRMEndHeader    = "STRME"
	STRMHeaderLen    = len(STRMHeader)
	STRMEndHeaderLen = len(STRMEndHeader)
)

// StreamWriter implements the io.WriteCloser for the stream writing of large data
// into small sizable chunks. Each message is tagged with a defined consistent id.
type StreamWriter struct {
	c        mnet.Client
	streamId string
	chunk    int
	total    int
	live     int64
	offset   int

	cm sync.Mutex
	cw io.WriteCloser
}

// NewStreamWriter returns a new instance of StreamWriter type for the provided Client
// with the associated chunk size.
func NewStreamWriter(c mnet.Client, streamId string, total int, chunk int) *StreamWriter {
	return &StreamWriter{
		c:        c,
		chunk:    chunk,
		total:    total,
		streamId: streamId,
	}
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

	header := fmt.Sprintf("%s:%s:%d:%d\r\n", STRMEndHeader, c.streamId, c.total, c.offset)
	headerSlice := []byte(header)

	var cw io.WriteCloser
	var err error

	// if we have no writer, make one else use existing one.
	c.cm.Lock()
	if c.cw == nil {
		c.cw, err = c.c.Write(len(header))
	}
	cw = c.cw
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
		if err == done.ErrLimitExceeded {
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
// Write does a few tricks underneath
func (c *StreamWriter) Write(p []byte) (int, error) {
	if atomic.LoadInt64(&c.live) == 1 {
		return 0, mnet.ErrStreamerClosed
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

		if err == done.ErrLimitExceeded {
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
	header := fmt.Sprintf(
		"%s:%s:%d:%d:%d:%d\r\n",
		STRMHeader,
		c.streamId,
		c.total,
		c.chunk,
		c.offset,
		c.offset+c.chunk,
	)

	chunkedLen := c.chunk + len(header)
	newWriter, err := c.c.Write(chunkedLen)
	if err != nil {
		return 0, err
	}

	c.cw = newWriter

	return newWriter.Write([]byte(header))
}
