package internal

import (
	"errors"
	"sync"

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
	mu   sync.RWMutex
	head *messageTomb
	tail *messageTomb
}

// Parse implements the necessary procedure for parsing incoming data and
// appropriately splitting them accordingly to their respective parts.
func (smp *TaggedMessages) Parse(d []byte) error {
	msg := messagePool.Get().(*messageTomb)
	msg.Data = d

	smp.mu.Lock()
	defer smp.mu.Unlock()

	if smp.head == nil && smp.tail == nil {
		smp.head = msg
		smp.tail = msg
		return nil
	}

	smp.tail.Next = msg
	smp.tail = msg
	return nil
}

// Next returns the next message saved on the parsers linked list.
func (smp *TaggedMessages) Next() ([]byte, error) {
	smp.mu.RLock()
	if smp.tail == nil && smp.head == nil {
		smp.mu.RUnlock()
		return nil, mnet.ErrNoDataYet
	}

	defer smp.mu.RUnlock()

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

	messagePool.Put(head)

	return data, nil
}
