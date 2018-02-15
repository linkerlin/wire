package internal

import (
	"bytes"
	"errors"
	"sync"
	"time"

	"encoding/binary"

	"strconv"

	"github.com/influx6/faux/pools/seeker"
	"github.com/influx6/mnet"
)

const (
	HeaderLength  = 4
	MaxHeaderSize = uint32(4294967295)
)

// errors ...
var (
	ErrTotalExceeded   = errors.New("specified data size exceeded, rejecting write")
	ErrLimitExceeded   = errors.New("writer space exceeded")
	ErrNotStream       = errors.New("data is not a stream transmission")
	ErrStreamDataParts = errors.New("invalid stream data, expected 2 part pieces")
	ErrHeaderLength    = errors.New("invalid header, header length not matching expected")
	ErrInvalidHeader   = errors.New("invalid header data max size: must be either int16, int32, int64 range")
)

var (
	messagePool = sync.Pool{New: func() interface{} { return new(messageTomb) }}
)

// Streamed embodies data related to a Streamed contents.
type Streamed struct {
	Meta      mnet.Meta
	ts        time.Time
	data      []byte
	completed bool
	written   int
	total     int
	available int
}

// StreamedMessages implements the parsing of Streamed messages which
// are split into different sets, and will collected all messages,
// till each it reached it's end state which then moves it to a set of
// finished products.
type StreamedMessages struct {
	keepfailed bool
	expr       time.Duration
	sl         sync.Mutex
	failed     map[string]*Streamed
	streams    map[string]*Streamed
	rl         sync.Mutex
	ready      []string
}

// NewStreamedMessages returns a new instance of StreamedMessages. It uses the provided
// expiration duration to spun a period check of all Streamed and incomplete data Streamed
// being cached, which then gets removed.
func NewStreamedMessages(expiration time.Duration, keepfailed bool) *StreamedMessages {
	return &StreamedMessages{
		keepfailed: keepfailed,
		expr:       expiration,
		streams:    make(map[string]*Streamed),
	}
}

// Next returns the next complete message stream which has being
// parsed from the underline message queue.
func (smp *StreamedMessages) Next() ([]byte, mnet.Meta, error) {
	var meta mnet.Meta
	var next string

	smp.rl.Lock()
	readyTotal := len(smp.ready)
	if readyTotal == 0 {
		smp.rl.Unlock()

		// Remove all aged streams.
		smp.removeAged()
		return nil, meta, mnet.ErrNoDataYet
	}

	next = smp.ready[0]
	if readyTotal > 1 {
		smp.ready = smp.ready[1:]
	} else {
		smp.ready = smp.ready[:0]
	}
	smp.rl.Unlock()

	smp.sl.Lock()
	item := smp.streams[next]
	delete(smp.streams, next)
	smp.sl.Unlock()

	// Remove all aged streams.
	smp.removeAged()

	return item.data, item.Meta, nil
}

// FailedStreams returns a map of all streams whoes data failed completion.
// This function is a one-time call, and the returned streamed map reference
// removed from the StreamedMessage.
func (smp *StreamedMessages) FailedStreams() map[string]*Streamed {
	smp.sl.Lock()
	defer smp.sl.Unlock()

	failed := smp.failed
	smp.failed = make(map[string]*Streamed)
	return failed
}

func (smp *StreamedMessages) removeAged() {
	smp.sl.Lock()
	defer smp.sl.Unlock()

	now := time.Now()

	// check every stream for aged/staled Streamed whoes last update
	// time has surpassed the expiration duration allowed.
	for id, stream := range smp.streams {
		if stream.ts.Sub(now) <= smp.expr || stream.completed {
			continue
		}

		stream.data = stream.data[:0]
		if smp.keepfailed {
			smp.failed[id] = stream
		}

		delete(smp.streams, id)
	}
}

var (
	ctrl        = []byte("\r\n")
	colon       = []byte(":")
	beginHeader = []byte(STRMHeader)
	endHeader   = []byte(STRMEndHeader)
)

// Parse implements the necessary procedure for parsing incoming data and
// appropriately splitting them accordingly to their respective parts.
func (smp *StreamedMessages) Parse(d []byte) error {
	if !bytes.HasPrefix(d, beginHeader) && !bytes.HasPrefix(d, endHeader) {
		return ErrNotStream
	}

	parts := bytes.SplitN(d, ctrl, 2)
	if len(parts) != 2 {
		return ErrStreamDataParts
	}

	header, data := parts[0], parts[1]
	headerParts := bytes.Split(header, colon)
	if len(headerParts) == 0 || (len(headerParts) != 4 && len(headerParts) != 6) {
		return ErrStreamDataParts
	}

	var id string
	var err error
	var total, chunk, offset, endOffset int64

	if total, err = strconv.ParseInt(string(headerParts[2]), 10, 64); err != nil {
		return err
	}

	var ok bool
	var isNew bool
	var stream *Streamed

	id = string(headerParts[1])
	smp.sl.Lock()
	if stream, ok = smp.streams[id]; !ok {
		isNew = true
		stream = new(Streamed)
		stream.total = int(total)
		stream.data = make([]byte, 0, stream.total)
		stream.data = stream.data[:stream.total]
	}
	smp.sl.Unlock()

	stream.ts = time.Now()

	if len(data) != 0 {
		stream.written += copy(stream.data[stream.written:stream.total], data)
	}

	// First convert header data so we can appropriately get necessary stream facts.
	headerType := string(headerParts[0])
	switch headerType {
	case STRMHeader:
		if chunk, err = strconv.ParseInt(string(headerParts[3]), 10, 64); err != nil {
			return err
		}

		if offset, err = strconv.ParseInt(string(headerParts[4]), 10, 64); err != nil {
			return err
		}

		if endOffset, err = strconv.ParseInt(string(headerParts[5]), 10, 64); err != nil {
			return err
		}

		if isNew {
			stream.Meta.Id = id
			//stream.Meta.IsStream = true
			stream.Meta.Total = total
			stream.Meta.Chunk = chunk
		}

		stream.Meta.Parts = append(stream.Meta.Parts, mnet.Part{Begin: offset, End: endOffset})

	case STRMEndHeader:
		if offset, err = strconv.ParseInt(string(headerParts[3]), 10, 64); err != nil {
			return err
		}

		smp.rl.Lock()
		smp.ready = append(smp.ready, id)
		smp.rl.Unlock()

		stream.completed = true
		stream.Meta.Parts = append(stream.Meta.Parts, mnet.Part{Begin: offset, End: offset})
	}

	smp.sl.Lock()
	smp.streams[id] = stream
	smp.sl.Unlock()

	return nil
}

// messageTomb defines a single node upon a linked list which
// contains it's data and a link to the next messageTomb node.
type messageTomb struct {
	Next *messageTomb
	Data []byte
}

// TaggedMessages implements a parser which parses incoming messages
// as a series of [MessageSizeLength][Message] joined together, which it
// splits apart into individual message blocks. The parser keeps a series of internal
// linked list which contains already processed message, which has a endless length
// value which allows appending new messages as they arrive.
type TaggedMessages struct {
	scratch *seeker.BufferedPeeker
	mu      sync.RWMutex
	head    *messageTomb
	tail    *messageTomb
}

// Parse implements the necessary procedure for parsing incoming data and
// appropriately splitting them accordingly to their respective parts.
func (smp *TaggedMessages) Parse(d []byte) error {
	if smp.scratch == nil {
		smp.scratch = seeker.NewBufferedPeeker(nil)
	}

	smp.scratch.Reset(d)

	for smp.scratch.Area() > 0 {
		nextdata := smp.scratch.Next(HeaderLength)
		if len(nextdata) < HeaderLength {
			smp.scratch.Reset(nil)
			return ErrHeaderLength
		}

		nextSize := int(binary.BigEndian.Uint32(nextdata))

		// If scratch is zero and we do have count data, maybe we face a unfinished write.
		if smp.scratch.Area() == 0 {
			smp.scratch.Reset(nil)
			return ErrInvalidHeader
		}

		if nextSize > smp.scratch.Area() {
			smp.scratch.Reset(nil)
			return ErrInvalidHeader
		}

		next := smp.scratch.Next(nextSize)

		msg := messagePool.New().(*messageTomb)
		msg.Data = next
		smp.addMessage(msg)
	}

	return nil
}

// Next returns the next message saved on the parsers linked list.
func (smp *TaggedMessages) Next() ([]byte, error) {
	smp.mu.RLock()
	if smp.tail == nil && smp.head == nil {
		smp.mu.RUnlock()
		return nil, mnet.ErrNoDataYet
	}

	head := smp.head
	if smp.tail == head {
		smp.tail = nil
		smp.head = nil
	} else {
		next := head.Next
		head.Next = nil
		smp.head = next
	}

	data := head.Data

	head.Data = nil
	smp.mu.RUnlock()

	messagePool.Put(head)

	return data, nil
}

func (smp *TaggedMessages) addMessage(m *messageTomb) {
	smp.mu.Lock()

	if smp.head == nil && smp.tail == nil {
		smp.head = m
		smp.tail = m
		smp.mu.Unlock()
		return
	}

	smp.tail.Next = m
	smp.tail = m
	smp.mu.Unlock()
}

//***************************************************************
// Custom functions
//***************************************************************
