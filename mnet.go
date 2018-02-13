package mnet

import (
	"errors"
	"io"
	"net"

	"github.com/influx6/faux/metrics"
)

//*************************************************************************
// Method Function types
//*************************************************************************

// ConnHandler defines a function which will process a incoming net.Conn,
// else if error is returned then the net.Conn is closed.
type ConnHandler func(Client) error

// ReaderFunc defines a function which takes giving incoming Client and returns associated
// data and error.
type ReaderFunc func(Client) ([]byte, error)

// WriteFunc defines a function type which takes a client, the max size of data to be written
// and returns a writer through which it receives data for only that specific size.
type WriteFunc func(Client, int) (io.WriteCloser, error)

// WriteStreamFunc defines a function type which takes a client, a total size of data to written
// and the max chunk size to split all data written to its writer. This allows us equally use
// the writer returned to stream large data size whilst ensuring that we still transfer in small
// sizes with minimal wait.
type StreamFunc func(Client, string, int, int) (io.WriteCloser, error)

// SiblingsFunc defines a function which returns a list of sibling funcs.
type SiblingsFunc func(Client) ([]Client, error)

// AddrFunc defines a function type which returns a net.Addr for a client.
type AddrFunc func(Client) (net.Addr, error)

// ClientFunc defines a function type which receives a Client type and
// returns a possible error.
type ClientFunc func(Client) error

// InfoFunc defines a function type which returns a Info struct
// for giving client.
type InfoFunc func(Client) Info

// StateFunc defines a function type which receives a Client type and
// returns a boolean value.
type StateFunc func(Client) bool

// ReconnectionFunc defines a function type which receives a Client pointer type and
// is responsible for the reconnection of the client client connection.
type ReconnectionFunc func(Client, string) error

// StatisticsFunc defines a function type which returns a Statistics
// structs related to the user.
type StatisticsFunc func(Client) (ClientStatistic, error)

// errors ...
var (
	ErrNotSupported                  = errors.New("feature not supported")
	ErrStreamerClosed                = errors.New("data stream writer is closed")
	ErrClientAlreadyExists           = errors.New("client with id exists")
	ErrFailedToRecieveInfo           = errors.New("failed to receive info")
	ErrFailedToCompleteHandshake     = errors.New("failed to complete handshake")
	ErrNoDataYet                     = errors.New("data is not yet available for reading")
	ErrAlreadyClosed                 = errors.New("already closed connection")
	ErrReadNotAllowed                = errors.New("reading not allowed")
	ErrLiveCheckNotAllowed           = errors.New("live status not allowed or supported")
	ErrWriteNotAllowed               = errors.New("data writing not allowed")
	ErrNoClusterServicing            = errors.New("cluster connections not servicable")
	ErrAlreadyServiced               = errors.New("cluster connections already serviced")
	ErrNoHostNameInAddr              = errors.New("addr must have hostname")
	ErrStillConnected                = errors.New("connection still active")
	ErrReadFromNotAllowed            = errors.New("reading from a addr not allowed")
	ErrWriteToAddrNotAllowed         = errors.New("data writing to a addr not allowed")
	ErrFlushToAddrNotAllowed         = errors.New("write flushing to target addr not allowed")
	ErrBufferExpansionNotAllowed     = errors.New("buffer expansion not allowed")
	ErrCloseNotAllowed               = errors.New("closing not allowed")
	ErrFlushNotAllowed               = errors.New("write flushing not allowed")
	ErrSiblingsNotAllowed            = errors.New("siblings retrieval not allowed")
	ErrStatisticsNotProvided         = errors.New("statistics not provided")
	ErrAddrNotProvided               = errors.New("net.Addr is not provided")
	ErrClientReconnectionUnavailable = errors.New("client reconnection not available")
)

//*************************************************************************
// Stream Type and Interface
//*************************************************************************

// Part embodies  offset information for a giving data stream.
type Part struct {
	Begin int64
	End   int64
}

// Meta embodies meta data used to contain data related to
// a stream.
type Meta struct {
	Chunk int64
	Total int64
	Id    string
	Parts []Part
}

// StreamReader defines an interface type which exposes a read method
// that returns a data with associated meta information if it was
// streamed as collective chunks over the wire, else returning
// an error if any occured or ErrNoDataYet, if no data is
// currently available.
type StreamReader interface {
	Read() ([]byte, *Meta, error)
}

//*************************************************************************
// Network and Client Stats
//*************************************************************************

// NetworkStatistic defines a struct ment to hold granular information regarding
// server network activities.
type NetworkStatistic struct {
	ID           string
	LocalAddr    net.Addr
	RemoteAddr   net.Addr
	TotalClients int64
	TotalClosed  int64
	TotalOpened  int64
}

// ClientStatistic defines a struct meant to hold granular information regarding
// client network activities.
type ClientStatistic struct {
	ID              string
	Local           net.Addr
	Remote          net.Addr
	MessagesRead    int64
	MessagesWritten int64
	BytesWritten    int64
	BytesRead       int64
	BytesFlushed    int64
	Reconnects      int64
}

// Info emodies a struct to contain specific info related to a giving connection.
type Info struct {
	Cluster    bool
	ServerNode bool
	ID         string
	ServerAddr string
}

//*************************************************************************
// mnet.Client Method and Implementation
//*************************************************************************

// Client holds a given information regarding a given network connection.
type Client struct {
	ID               string
	NID              string
	Metrics          metrics.Metrics
	LocalAddrFunc    AddrFunc
	RemoteAddrFunc   AddrFunc
	ReaderFunc       ReaderFunc
	WriteFunc        WriteFunc
	InfoFunc         InfoFunc
	CloseFunc        ClientFunc
	LiveFunc         ClientFunc
	FlushFunc        ClientFunc
	IsClusterFunc    StateFunc
	SiblingsFunc     SiblingsFunc
	StatisticFunc    StatisticsFunc
	ReconnectionFunc ReconnectionFunc
}

// Info returns associated info of giving client.
// WARNING: Note that Info may in the case of a client representing
// a another Server will return information for that server and not
// information regarding it's local address. Use Clent.RemoteAddr()
// and Client.LocalAddr() to get accurate on-machine address for
// client.
func (c Client) Info() Info {
	if c.InfoFunc == nil {
		return Info{ID: c.ID}
	}

	return c.InfoFunc(c)
}

// LocalAddr returns local address associated with given client.
func (c Client) LocalAddr() (net.Addr, error) {
	c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.With("network", c.NID),
		metrics.Message("Client.LocalAddr"),
	)

	if c.LocalAddrFunc == nil {
		return nil, ErrAddrNotProvided
	}

	addr, err := c.LocalAddrFunc(c)
	if err != nil {
		c.Metrics.Emit(
			metrics.Error(err),
			metrics.WithID(c.ID),
			metrics.With("network", c.NID),
			metrics.Message("Client.LocalAddr"),
		)
		return nil, err
	}

	return addr, nil
}

// RemoteAddr returns remote address associated with given client.
func (c Client) RemoteAddr() (net.Addr, error) {
	c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.With("network", c.NID),
		metrics.Message("Client.RemoteAddr"),
	)

	if c.RemoteAddrFunc == nil {
		return nil, ErrAddrNotProvided
	}

	addr, err := c.RemoteAddrFunc(c)
	if err != nil {
		c.Metrics.Emit(
			metrics.Error(err),
			metrics.WithID(c.ID),
			metrics.With("network", c.NID),
			metrics.Message("Client.RemoteAddr"),
		)
		return nil, err
	}

	return addr, nil
}

// IsCluster returns true if giving connection is a server-to-server
// connection or false if its a client-to-cluster connection.
// NOTE: It will by default returns false if no function for Client.IsClusterFunc
// is provided.
func (c Client) IsCluster() bool {
	if c.IsClusterFunc == nil {
		return false
	}

	return c.IsClusterFunc(c)
}

// Live returns an error if client is not currently live or connected to
// network. Allows to know current status of client.
func (c Client) Live() error {
	c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.Message("Client.Live"),
		metrics.With("network", c.NID),
	)
	if c.LiveFunc == nil {
		return ErrLiveCheckNotAllowed
	}

	if err := c.LiveFunc(c); err != nil {
		c.Metrics.Emit(
			metrics.Error(err),
			metrics.WithID(c.ID),
			metrics.Message("Client.Live"),
			metrics.With("network", c.NID),
		)
		return err
	}

	return nil
}

// Reconnect attempts to reconnect with external endpoint.
// Also allows provision of alternate address to reconnect with.
// NOTE: Not all may implement this has it's optional.
func (c Client) Reconnect(altAddr string) error {
	c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.Message("Client.Reconnect"),
		metrics.With("network", c.NID),
		metrics.With("alternate_addr", altAddr),
	)
	if c.ReconnectionFunc == nil {
		return ErrClientReconnectionUnavailable
	}

	if err := c.ReconnectionFunc(c, altAddr); err != nil {
		c.Metrics.Emit(
			metrics.WithID(c.ID),
			metrics.Message("Client.Reconnect"),
			metrics.Error(err),
			metrics.With("alternate_addr", altAddr),
			metrics.With("network", c.NID),
		)
		return err
	}

	return nil
}

// Read reads the underline data into the provided slice.
// NOTE: Read does not differentiate between contents written into
// the client through either client.Write or client.Stream, this is
// left to the implementer to handle. Most would probably be using
// implementations in the internal package (StreamWriter).
//
// NOTE: You can also wrap a client instance with the internal package's
// StreamReader, which caters to organize received streams.
//
// Internal Package: (see github.com/influx6/mnet/blob/master/internal)
func (c Client) Read() ([]byte, error) {
	if c.ReaderFunc == nil {
		return nil, ErrReadNotAllowed
	}

	return c.ReaderFunc(c)
}

// Write returns a writer to writes provided data of specified size into
// underline connection. The writer returned is useful for streaming a single
// small to medium scale data within good desired limits. If you have excessive large
// data which transfer will take massive time, consider using `Client.Streamr`,
// which allows streaming very large data in chunk size that are then collected and
// built together on the receiver side.
func (c Client) Write(toWriteSize int) (io.WriteCloser, error) {
	if c.WriteFunc == nil {
		return nil, ErrWriteNotAllowed
	}

	return c.WriteFunc(c, toWriteSize)
}

// Flush sends all accumulated message within clients buffer into
// connection.
func (c Client) Flush() error {
	if c.FlushFunc == nil {
		return ErrFlushNotAllowed
	}

	return c.FlushFunc(c)
}

// Statistics returns statistics associated with client.j
func (c Client) Statistics() (ClientStatistic, error) {
	c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.Message("Client.Statistics"),
		metrics.With("network", c.NID),
	)

	if c.StatisticFunc == nil {
		return ClientStatistic{}, ErrStatisticsNotProvided
	}

	return c.StatisticFunc(c)
}

// Close closes the underline client connection.
func (c Client) Close() error {
	c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.Message("Client.Close"),
		metrics.With("network", c.NID),
	)

	if c.CloseFunc == nil {
		return ErrCloseNotAllowed
	}

	if err := c.CloseFunc(c); err != nil {
		c.Metrics.Emit(
			metrics.WithID(c.ID),
			metrics.Message("Client.Close"),
			metrics.Error(err),
			metrics.With("network", c.NID),
		)
		return err
	}

	return nil
}

// Others returns other client associated with the source of this client.
// NOTE: Not all may implement this has it's optional.
// WARNING: And those who implement this method must ensure the Read method do not
// exists, to avoid conflict of internal behaviour. More so, only the
// server client handler should ever have access to Read/ReadFrom methods.
func (c Client) Others() ([]Client, error) {
	defer c.Metrics.Emit(
		metrics.WithID(c.ID),
		metrics.Message("Client.Others"),
		metrics.With("network", c.NID),
	)

	if c.SiblingsFunc == nil {
		return nil, ErrSiblingsNotAllowed
	}

	return c.SiblingsFunc(c)
}
