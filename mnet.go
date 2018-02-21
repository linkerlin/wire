package mnet

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"time"

	"github.com/influx6/faux/metrics"
)

// errors ...
var (
	ErrNotSupported                  = errors.New("feature not supported")
	ErrStreamerClosed                = errors.New("data stream writer is closed")
	ErrClientAlreadyExists           = errors.New("client with id exists")
	ErrFailedToRecieveInfo           = errors.New("failed to receive CINFO response")
	ErrFailedToRecieveInfoReq        = errors.New("failed to receive CINFO request")
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
// Network Type
//*************************************************************************

// Network defines an interface type which is defines specific function
// to startup a underline network system.
type Network interface {
	Wait()
	Addrs() net.Addr
	Start(context.Context) error
}

// ClusteredNetwork defines an interface that embeds the Network interface
// and provides a method to add another cluster network as a connection
// to an implementation of the Network interface through it's AddCluster
// method.
type ClusteredNetwork interface {
	Network
	AddCluster(string) error
}

//*************************************************************************
// Client Service Type
//*************************************************************************

// ClientService defines a interface which represent a structure which is
// suited to handle/serve a single instance of a Client connection.
// This should be used in conjunction with the various network implementations
// of the Client type, where the ClientService.ServeClient method would be
// goroutined to handle and service all requests and response related to
// a connected Client.
type ClientService interface {
	ServeClient(Client) error
}

//*************************************************************************
// Hook Type
//*************************************************************************

// Hook defines an interface which exposes methods to receive notifications
// of the connection/disconnect of clients and network.
type Hook interface {
	NetworkClosed()
	NetworkStarted()
	NodeAdded(Client)
	ClusterAdded(Client)
	NodeDisconnected(Client)
	ClusterDisconnected(Client)
}

//*************************************************************************
// Data Reader Function Type
//*************************************************************************

// ConnHandler defines a function which will process a incoming net.Conn,
// else if error is returned then the net.Conn is closed.
type ConnHandler func(Client) error

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

// Meta are small facts and details contained in a map sent along with
// a info object.
type Meta map[string]interface{}

// Info emodies a struct to contain specific info related to a giving network,
// it also contains data relating to known network servers by a giving network.
type Info struct {
	Cluster      bool
	ServerNode   bool
	ID           string
	ServerAddr   string
	MaxBuffer    int64
	MinBuffer    int64
	ClusterNodes []Info
	Meta         Meta
}

//*************************************************************************
// Method Function types
//*************************************************************************

// ReaderFunc defines a function which takes giving incoming Client and returns associated
// data and error.
type ReaderFunc func() ([]byte, error)

// WriteFunc defines a function type takes the max size of data to be written
// and returns a writer through which it receives data for only that specific size.
type WriteFunc func(int) (io.WriteCloser, error)

// WriteStreamFunc defines a function type which takes a total size of data to written
// and the max chunk size to split all data written to its writer. This allows us equally use
// the writer returned to stream large data size whilst ensuring that we still transfer in small
// sizes with minimal wait.
type StreamFunc func(string, int, int) (io.WriteCloser, error)

// SiblingsFunc defines a function which returns a list of sibling funcs.
type SiblingsFunc func() ([]Client, error)

// AddrFunc defines a function type which returns a net.Addr for a client.
type AddrFunc func() (net.Addr, error)

// ClientFunc defines a function type which .Meta
// returns a possible error.
type ClientFunc func() error

// InfoFunc defines a function type which returns a Info struct
// for giving client.
type InfoFunc func() Info

// StateFunc defines a function type which returns a boolean value for a state.
type StateFunc func() bool

// StateFunc defines a function type which returns a boolean value for a state
// and returns an error as well.
type StateWithErrorFunc func() (bool, error)

// ReconnectionFunc defines a function type which is responsible for
// the reconnection of the client client connection.
type ReconnectionFunc func(string) error

// StatisticsFunc defines a function type which returns a Statistics
// structs related to the user.
type StatisticsFunc func() (ClientStatistic, error)

//*************************************************************************
// mnet.Client Method and Implementation
//*************************************************************************

// Client holds a given information regarding a given network connection.
type Client struct {
	ID               string
	NID              string
	LocalAddrFunc    AddrFunc
	RemoteAddrFunc   AddrFunc
	ReaderFunc       ReaderFunc
	WriteFunc        WriteFunc
	InfoFunc         InfoFunc
	CloseFunc        ClientFunc
	LiveFunc         ClientFunc
	FlushFunc        ClientFunc
	IsClusterFunc    StateFunc
	HasPendingFunc   StateFunc
	SiblingsFunc     SiblingsFunc
	StatisticFunc    StatisticsFunc
	ReconnectionFunc ReconnectionFunc
	Metrics          metrics.Metrics
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

	return c.InfoFunc()
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

	addr, err := c.LocalAddrFunc()
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

	addr, err := c.RemoteAddrFunc()
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
// NOTE: It will by default returns false if no function for
// Client.IsClusterFunc is provided.
func (c Client) IsCluster() bool {
	if c.IsClusterFunc == nil {
		return false
	}

	return c.IsClusterFunc()
}

// HasPending returns true/false if any data is still awaiting
// flushing into the underline network writer.
func (c Client) HasPending() bool {
	if c.HasPendingFunc == nil {
		return false
	}

	return c.HasPendingFunc()
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

	if err := c.LiveFunc(); err != nil {
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

	if err := c.ReconnectionFunc(altAddr); err != nil {
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

	return c.ReaderFunc()
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

	return c.WriteFunc(toWriteSize)
}

// Flush sends all accumulated message within clients buffer into
// connection.
func (c Client) Flush() error {
	if c.FlushFunc == nil {
		return ErrFlushNotAllowed
	}

	return c.FlushFunc()
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

	return c.StatisticFunc()
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

	if err := c.CloseFunc(); err != nil {
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

	return c.SiblingsFunc()
}

//************************************************************************
// RetryPolicy interface and Implementation
//************************************************************************

// RetryPolicy defines a interface which exposes a single method which is
// called to perform a giving action until some criteria is met or the
// conditions for possible retries is become invalid thereby returning an
// error.
type RetryPolicy interface {
	Retry() error
}

// RetryAction defines a function type which called would return an error
// if it's internal operation or state fails.
type RetryAction func() error

// DelayJudge defines a function which takes a giving count value
// returning a time.Duration usable to wait before attempting a
// another call.
type DelayJudge func(int) time.Duration

// FunctionRetryPolicy returns a new RetryPolicy which uses provided function
// to retry said actions till no error is received or till we max out possible
// allowed retries.
func FunctionPolicy(max int, action RetryAction, delay DelayJudge) RetryPolicy {
	return &fnRetryPolicy{
		delay:  delay,
		action: action,
		max:    max,
	}
}

type fnRetryPolicy struct {
	max    int
	now    int
	delay  DelayJudge
	action RetryAction
}

// Retry attempts to run giving action until success or else
// will attempt to generate continuous longer sleep durations
// to wait before next try. If it has expired all possible tries
// then it stops, else will exhaust all possible tries.
func (fn *fnRetryPolicy) Retry() error {
	if fn.now >= fn.max {
		return errors.New("already expired")
	}

	for {
		fn.now++
		if nextDelay := fn.delay(fn.now); nextDelay > 0 {
			time.Sleep(nextDelay)
		}

		if err := fn.action(); err == nil {
			fn.now = 0
			return nil
		}
	}
	return errors.New("expired retries")
}

// ExponentialDelay returns a DelayJudge which for every giving time
// will expand it's next duration by the provided exampnd value times
// an exponential calculation of the current try value.
func ExponentialDelay(expand time.Duration) DelayJudge {
	return func(i int) time.Duration {
		return time.Duration(math.Exp2(float64(i))) * expand
	}
}
