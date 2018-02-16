package internal

import (
	"errors"
	"io"

	"sync/atomic"

	"encoding/binary"

	"github.com/influx6/mnet"
)

const (
	nostate int64 = iota
	pending
)

const (
	max2 uint16 = 65535
	max4 uint32 = 4294967295
	max8 uint64 = 18446744073709551615
)

// errors ...
var (
	ErrInvalidReadState    = errors.New("invalid read state encountered")
	ErrInvalidReadOp       = errors.New("reader read 0 bytes")
	ErrUncompletedTransfer = errors.New("reader data is less than expected size")
)

// LengthReader implements a custom byte reader which wraps a provided io.Reader
// and attempts to read necessary incoming data where each data even if batched
// together always has a `LENGHTOFData` attached to it's original
// Length reader respects the follow header size (2 for uint16, 4 for uint32, 8 for uint64).
// It will handle each respective length sizes based on the headerLen value provided and
// errors will be returned if the giving reader returns no data or fails to fully read
// all expected data as specified by header received.
type LengthReader struct {
	header int
	target int
	rem    int
	last   int
	state  int64
	r      io.Reader
	buff   []byte
	area   []byte
}

// NewLengthReader returns a new instance of a LengthReader.
func NewLengthReader(r io.Reader, headerLen int) *LengthReader {
	return &LengthReader{
		r:      r,
		header: headerLen,
		state:  nostate,
		last:   mnet.MinBufferSize,
		area:   make([]byte, headerLen),
	}
}

// Read returns the next available bytes received from the underline reader
// if it encounters any io.EOF errors, those will be returned as well with
// any read data. If Read meets any incoming data with no size header, it will
// return an ErrInvalidHeader in such cases.
func (lr *LengthReader) Read() ([]byte, error) {
	return lr.read()
}

func (lr *LengthReader) read() ([]byte, error) {
	lastState := atomic.LoadInt64(&lr.state)
	switch lastState {
	case nostate:
		if err := lr.readHeader(); err != nil {
			lr.reset()
			return nil, err
		}

		return lr.read()
	case pending:
		return lr.readBody()
	}

	return nil, ErrInvalidReadState
}

func (lr *LengthReader) readHeader() error {
	n, err := lr.r.Read(lr.area)
	if err != nil {
		return err
	}

	if n < lr.header {
		return ErrHeaderLength
	}

	switch lr.header {
	case 2:
		lr.target = int(binary.BigEndian.Uint16(lr.area))
		if uint16(lr.target) > max2 {
			return ErrInvalidHeader
		}
	case 4:
		lr.target = int(binary.BigEndian.Uint32(lr.area))
		if uint32(lr.target) > max4 {
			return ErrInvalidHeader
		}
	case 8:
		lr.target = int(binary.BigEndian.Uint64(lr.area))
		if uint64(lr.target) > max8 {
			return ErrInvalidHeader
		}
	}

	lr.last = 0
	lr.rem = lr.target
	lr.buff = make([]byte, lr.target)
	atomic.StoreInt64(&lr.state, pending)
	return nil
}

func (lr *LengthReader) readBody() ([]byte, error) {
	if lr.last == lr.target && lr.rem == 0 {
		data := lr.buff[0:lr.target]
		lr.reset()
		return data, nil
	}

	sector := lr.buff[lr.last : lr.last+lr.rem]
	n, err := lr.r.Read(sector)
	if err != nil && err != io.EOF {
		lr.reset()
		return nil, err
	}

	if n == 0 {
		if lr.rem > 0 {
			lr.reset()
			return nil, ErrUncompletedTransfer
		}

		lr.reset()
		return nil, ErrInvalidReadOp
	}

	lr.last += n
	lr.rem -= n

	return lr.readBody()
}

func (lr *LengthReader) reset() {
	lr.target = 0
	lr.last = 0
	lr.rem = 0
	lr.buff = nil
	atomic.StoreInt64(&lr.state, nostate)
}
