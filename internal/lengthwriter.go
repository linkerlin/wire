package internal

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/influx6/faux/pools/pbytes"
)

var (
	bit2Pool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 2)
		},
	}

	bit4Pool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 4)
		},
	}

	bit8Pool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 8)
		},
	}

	bytespool = pbytes.NewBytesPool(218, 20)
)

//**********************************************************************
// LengthWriter
//**********************************************************************

// LengthWriter implements a io.WriteCloser which prepends/prefixes
// the writing size of data into the provided writer before written
// the data itself.
type LengthWriter struct {
	n    int
	max  int
	ty   int
	area []byte
	buff []byte
	w    io.Writer
}

// NewLengthWriter returns a new instance of a LengthWriter which
// appends it's dataLength into a giving sized through size byteslice
// which represent either a int16(where size is 2), int32(where size is 4),
// int64(where size is 8) is used.
func NewLengthWriter(w io.Writer, size int, dataLength int) *LengthWriter {
	var area []byte

	switch size {
	case 2:
		area = bit2Pool.Get().([]byte)
	case 4:
		area = bit4Pool.Get().([]byte)
	case 8:
		area = bit8Pool.Get().([]byte)
	}

	return &LengthWriter{
		w:    w,
		ty:   size,
		max:  dataLength,
		area: area,
		buff: bytespool.Get(dataLength),
	}
}

// Reset resets io.Writer, size and data length to be used by writer for its operations.
func (lw *LengthWriter) Reset(w io.Writer, size int, dataLength int) {
	var area []byte

	switch size {
	case 2:
		area = bit2Pool.Get().([]byte)
	case 4:
		area = bit4Pool.Get().([]byte)
	case 8:
		area = bit8Pool.Get().([]byte)
	}

	lw.n = 0
	lw.w = w
	lw.ty = size
	lw.area = area
	lw.max = dataLength
	lw.buff = bytespool.Get(dataLength)
}

// Close closes this writer and flushes data into underline writer.
func (lw *LengthWriter) Close() error {
	if lw.n == 0 {
		switch lw.ty {
		case 2:
			bit2Pool.Put(lw.area)
		case 4:
			bit4Pool.Put(lw.area)
		case 8:
			bit8Pool.Put(lw.area)
		}

		lw.w = nil
		lw.area = nil
		lw.buff = nil
		return nil
	}

	switch lw.ty {
	case 2:
		binary.BigEndian.PutUint16(lw.area, uint16(lw.n))
	case 4:
		binary.BigEndian.PutUint32(lw.area, uint32(lw.n))
	case 8:
		binary.BigEndian.PutUint64(lw.area, uint64(lw.n))
	}

	sizeW, err := lw.w.Write(lw.area)
	if err != nil {
		return err
	}

	switch lw.ty {
	case 2:
		bit2Pool.Put(lw.area)
	case 4:
		bit4Pool.Put(lw.area)
	case 8:
		bit8Pool.Put(lw.area)
	}

	lw.area = nil

	dataW, err := lw.w.Write(lw.buff[0:lw.n])
	if err != nil {
		return err
	}

	bytespool.Put(lw.buff)

	lw.w = nil
	lw.buff = nil

	if sizeW+dataW != lw.max+lw.ty {
		return io.ErrShortWrite
	}

	return nil
}

// Write will attempt to copy data within provided slice into writers
// size constrained buffer. If data provided is more than available
// space then an ErrLimitExceeded is returned.
func (lw *LengthWriter) Write(d []byte) (int, error) {
	rem := lw.max - lw.n
	if len(d) > rem {
		return 0, ErrLimitExceeded
	}

	n := copy(lw.buff[lw.n:lw.max], d)
	lw.n += n
	return n, nil
}

//**********************************************************************
// LengthWriter
//**********************************************************************

// WriterAction defines a function type to be used when writer gets closed.
type WriterAction func(size []byte, data []byte) error

// ActionLengthWriter implements a io.WriteCloser which prepends/prefixes
// the writing size of data into the provided writer before written
// the data itself.
type ActionLengthWriter struct {
	n    int
	max  int
	ty   int
	area []byte
	buff []byte
	wx   WriterAction
}

// NewActionLengthWriter returns a new instance of a ActionLengthWriter which
// appends it's dataLength into a giving sized through size byteslice
// which represent either a int16(where size is 2), int32(where size is 4),
// int64(where size is 8) is used.
func NewActionLengthWriter(wx WriterAction, size int, dataLength int) *ActionLengthWriter {
	var area []byte

	switch size {
	case 2:
		area = bit2Pool.Get().([]byte)
	case 4:
		area = bit4Pool.Get().([]byte)
	case 8:
		area = bit8Pool.Get().([]byte)
	}

	return &ActionLengthWriter{
		wx:   wx,
		ty:   size,
		max:  dataLength,
		area: area,
		buff: bytespool.Get(dataLength),
	}
}

// Reset resets action function, size and data length to be used by writer for its operations.
func (lw *ActionLengthWriter) Reset(wx WriterAction, size int, dataLength int) {
	var area []byte

	switch size {
	case 2:
		area = bit2Pool.Get().([]byte)
	case 4:
		area = bit4Pool.Get().([]byte)
	case 8:
		area = bit8Pool.Get().([]byte)
	}

	lw.n = 0
	lw.wx = wx
	lw.ty = size
	lw.area = area
	lw.max = dataLength
	lw.buff = bytespool.Get(dataLength)
}

// Close closes this writer and flushes data into underline writer.
func (lw *ActionLengthWriter) Close() error {
	if lw.n == 0 {
		switch lw.ty {
		case 2:
			bit2Pool.Put(lw.area)
		case 4:
			bit4Pool.Put(lw.area)
		case 8:
			bit8Pool.Put(lw.area)
		}

		lw.wx = nil
		lw.area = nil
		lw.buff = nil
		return nil
	}

	switch lw.ty {
	case 2:
		binary.BigEndian.PutUint16(lw.area, uint16(lw.n))
	case 4:
		binary.BigEndian.PutUint32(lw.area, uint32(lw.n))
	case 8:
		binary.BigEndian.PutUint64(lw.area, uint64(lw.n))
	}

	err := lw.wx(lw.area, lw.buff[0:lw.n])

	bytespool.Put(lw.buff)

	switch lw.ty {
	case 2:
		bit2Pool.Put(lw.area)
	case 4:
		bit4Pool.Put(lw.area)
	case 8:
		bit8Pool.Put(lw.area)
	}

	lw.wx = nil
	lw.area = nil
	lw.buff = nil
	return err
}

// Write will attempt to copy data within provided slice into writers
// size constrained buffer. If data provided is more than available
// space then an ErrLimitExceeded is returned.
func (lw *ActionLengthWriter) Write(d []byte) (int, error) {
	rem := lw.max - lw.n
	if len(d) > rem {
		return 0, ErrLimitExceeded
	}

	n := copy(lw.buff[lw.n:lw.max], d)
	lw.n += n
	return n, nil
}
