package mudp

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"time"

	"bufio"

	"sync"

	"context"

	"github.com/influx6/faux/metrics"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/internal"
	uuid "github.com/satori/go.uuid"
)

// errors ..
var (
	ErrExpectedUDPConn = errors.New("net.Conn returned is not a *net.UDPConn")
)

// ConnectOptions defines a function type used to apply given
// changes to a *clientNetwork type
type ConnectOptions func(conn *clientConn)

// Metrics sets the metrics instance to be used by the client for
// logging.
func Metrics(m metrics.Metrics) ConnectOptions {
	return func(cm *clientConn) {
		cm.metrics = m
	}
}

// Dialer sets the ws.Dialer to used creating a connection
// to the server.
func Dialer(dialer *net.Dialer) ConnectOptions {
	return func(cm *clientConn) {
		cm.dialer = dialer
	}
}

// KeepAliveTimeout sets the client to use given timeout for it's connection net.Dialer
// keepAliveTimeout.
func KeepAliveTimeout(dur time.Duration) ConnectOptions {
	return func(cm *clientConn) {
		cm.keepTimeout = dur
	}
}

// DialTimeout sets the client to use given timeout for it's connection net.Dialer
// dial timeout.
func DialTimeout(dur time.Duration) ConnectOptions {
	return func(cm *clientConn) {
		cm.dialTimeout = dur
	}
}

// NetworkID sets the id used by the client connection for identifying the
// associated network.
func NetworkID(id string) ConnectOptions {
	return func(cm *clientConn) {
		cm.nid = id
	}
}

// ReadBuffer sets the read buffer to be used by the udp listener
// ensure this is set according to what is set on os else the
// udp listener will error out.
func ReadBuffer(b int) ConnectOptions {
	return func(cm *clientConn) {
		cm.readBuffer = b
	}
}

// Connect is used to implement the client connection to connect to a
// mtcp.Network. It implements all the method functions required
// by the Client to communicate with the server. It understands
// the message length header sent along by every message and follows
// suite when sending to server.
func Connect(addr string, ops ...ConnectOptions) (mnet.Client, error) {
	var c mnet.Client
	c.ID = uuid.NewV4().String()

	addr = netutils.GetAddr(addr)
	//host, _, _ := net.SplitHostPort(addr)

	network := new(clientConn)

	for _, op := range ops {
		op(network)
	}

	if network.network == "" {
		network.network = "udp"
	}

	if network.metrics == nil {
		network.metrics = metrics.New()
	}

	if network.dialer == nil {
		network.dialer = &net.Dialer{
			Timeout:   network.dialTimeout,
			KeepAlive: network.keepTimeout,
		}
	}

	network.parser = new(internal.TaggedMessages)
	network.br = internal.NewLengthRecvReader(nil, mnet.HeaderLength)

	c.NID = network.nid
	c.Metrics = network.metrics
	c.CloseFunc = network.close
	c.ReaderFunc = network.read
	c.WriteFunc = network.write
	c.FlushFunc = network.flush
	c.LiveFunc = network.isAlive
	c.StatisticFunc = network.stats
	c.LiveFunc = network.isAlive
	c.LocalAddrFunc = network.localAddr
	c.HasPendingFunc = network.hasPending
	c.RemoteAddrFunc = network.remoteAddr
	c.ReconnectionFunc = network.reconnect

	if err := network.reconnect(addr); err != nil {
		return c, err
	}

	return c, nil
}

// clientConn implements the client side udp connection client.
// It embeds the udpServerClient and adds extra methods to provide client
// side behaviours.
type clientConn struct {
	readBuffer int
	started    int64
	network    string

	dialer      *net.Dialer
	waiter      sync.WaitGroup
	keepTimeout time.Duration
	dialTimeout time.Duration
	metrics     metrics.Metrics

	ctx    context.Context
	cancel func()

	id       string
	nid      string
	mainAddr net.Addr
	target   net.Addr
	raddr    net.Addr
	laddr    net.Addr
	parser   *internal.TaggedMessages

	totalRead     int64
	totalWritten  int64
	totalFlushed  int64
	totalMsgWrite int64
	totalMsgRead  int64
	reconnects    int64
	closed        int64

	br   *internal.LengthRecvReader
	cu   sync.Mutex
	conn *net.UDPConn
	bu   sync.Mutex
	bw   *bufio.Writer
}

func (cn *clientConn) readIncoming(r io.Reader) error {
	cn.br.Reset(r)

	var data []byte
	for {
		size, err := cn.br.ReadHeader()
		if err != nil {
			if err != io.EOF {
				return err
			}

			return nil
		}

		data = make([]byte, size)
		_, err = cn.br.Read(data)
		if err != nil {
			return err
		}

		if err = cn.parser.Parse(data); err != nil {
			return err
		}
	}
}

func (cn *clientConn) localAddr() (net.Addr, error) {
	return cn.laddr, nil
}

func (cn *clientConn) remoteAddr() (net.Addr, error) {
	return cn.raddr, nil
}

func (cn *clientConn) read() ([]byte, error) {
	if err := cn.isAlive(); err != nil {
		return nil, err
	}

	atomic.AddInt64(&cn.totalMsgRead, 1)
	indata, err := cn.parser.Next()
	atomic.AddInt64(&cn.totalRead, int64(len(indata)))
	return indata, err
}

func (cn *clientConn) noop() error {
	return nil
}

func (cn *clientConn) stats() (mnet.ClientStatistic, error) {
	var stats mnet.ClientStatistic
	stats.ID = cn.id
	stats.Local = cn.laddr
	stats.Remote = cn.raddr
	stats.BytesRead = atomic.LoadInt64(&cn.totalRead)
	stats.MessagesRead = atomic.LoadInt64(&cn.totalMsgRead)
	stats.BytesWritten = atomic.LoadInt64(&cn.totalWritten)
	stats.BytesFlushed = atomic.LoadInt64(&cn.totalFlushed)
	stats.Reconnects = atomic.LoadInt64(&cn.reconnects)
	stats.MessagesWritten = atomic.LoadInt64(&cn.totalMsgWrite)
	return stats, nil
}

func (cn *clientConn) isStarted() bool {
	return atomic.LoadInt64(&cn.started) == 1
}

func (cn *clientConn) isAlive() error {
	if atomic.LoadInt64(&cn.closed) == 1 && cn.isStarted() {
		return mnet.ErrAlreadyClosed
	}
	return nil
}

func (cn *clientConn) hasPending() bool {
	if err := cn.isAlive(); err != nil {
		return false
	}

	cn.bu.Lock()
	defer cn.bu.Unlock()
	if cn.bw == nil {
		return false
	}

	return cn.bw.Buffered() > 0
}

// write returns an appropriate io.WriteCloser with appropriate size
// to contain data to be written to be connection.
func (cn *clientConn) flush() error {
	if err := cn.isAlive(); err != nil {
		return err
	}

	var conn net.Conn
	cn.cu.Lock()
	conn = cn.conn
	cn.cu.Unlock()

	if conn == nil {
		return mnet.ErrAlreadyClosed
	}

	cn.bu.Lock()
	defer cn.bu.Unlock()

	if cn.bw == nil {
		return mnet.ErrAlreadyClosed
	}

	buffered := cn.bw.Buffered()
	atomic.AddInt64(&cn.totalFlushed, int64(buffered))

	if buffered > 0 {
		if err := cn.bw.Flush(); err != nil {
			return err
		}
	}

	return nil
}

func (cn *clientConn) writeAction(size []byte, data []byte) error {
	atomic.AddInt64(&cn.totalWritten, 1)
	atomic.AddInt64(&cn.totalWritten, int64(len(data)))

	cn.bu.Lock()
	defer cn.bu.Unlock()

	if cn.bw == nil {
		return mnet.ErrAlreadyClosed
	}

	available := cn.bw.Available()
	buffered := cn.bw.Buffered()
	atomic.AddInt64(&cn.totalFlushed, int64(buffered))

	// size of next write.
	toWrite := buffered + len(data)

	// add size header
	toWrite += mnet.HeaderLength

	if toWrite > available {
		if err := cn.bw.Flush(); err != nil {
			return err
		}
	}

	if _, err := cn.bw.Write(size); err != nil {
		return err
	}

	if _, err := cn.bw.Write(data); err != nil {
		return err
	}

	return nil
}

func (cn *clientConn) writeTo(size int, addr net.Addr) (io.WriteCloser, error) {
	if err := cn.isAlive(); err != nil {
		return nil, err
	}

	var conn *net.UDPConn
	cn.cu.Lock()
	conn = cn.conn
	cn.cu.Unlock()

	if conn == nil {
		return nil, mnet.ErrAlreadyClosed
	}

	newWriter := actionWriterPool.Get().(*internal.ActionLengthWriter)
	newWriter.Reset(func(size []byte, data []byte) error {
		defer actionWriterPool.Put(newWriter)
		if addr == nil {
			_, err := conn.Write(size)
			if err == nil {
				return err
			}

			_, err = conn.Write(data)
			return err
		}

		_, err := conn.WriteTo(size, addr)
		if err != nil {
			return err
		}

		_, err = conn.WriteTo(data, addr)
		return err
	}, mnet.HeaderLength, size)

	return newWriter, nil
}

// write returns an appropriate io.WriteCloser with appropriate size
// to contain data to be written to be connection.
func (cn *clientConn) write(size int) (io.WriteCloser, error) {
	if err := cn.isAlive(); err != nil {
		return nil, err
	}

	var conn net.Conn
	cn.cu.Lock()
	conn = cn.conn
	cn.cu.Unlock()

	if conn == nil {
		return nil, mnet.ErrAlreadyClosed
	}

	newWriter := actionWriterPool.Get().(*internal.ActionLengthWriter)
	newWriter.Reset(func(size []byte, data []byte) error {
		err := cn.writeAction(size, data)
		actionWriterPool.Put(newWriter)
		return err
	}, mnet.HeaderLength, size)

	return newWriter, nil
}

func (cn *clientConn) close() error {
	if err := cn.isAlive(); err != nil {
		return mnet.ErrAlreadyClosed
	}

	cn.flush()

	cn.cu.Lock()
	if cn.conn == nil {
		cn.cu.Unlock()
		return mnet.ErrAlreadyClosed
	}

	err := cn.conn.Close()
	cn.cu.Unlock()

	if cn.cancel != nil {
		cn.cancel()
	}

	cn.waiter.Wait()

	cn.cu.Lock()
	cn.conn = nil
	cn.cu.Unlock()

	cn.bu.Lock()
	cn.bw = nil
	cn.bu.Unlock()

	return err
}

func (cn *clientConn) reconnect(addr string) error {
	if err := cn.isAlive(); err == nil && cn.isStarted() {
		return nil
	}

	defer atomic.StoreInt64(&cn.started, 1)

	cn.waiter.Wait()

	cn.ctx, cn.cancel = context.WithCancel(context.Background())

	if cn.remoteAddr == nil || (cn.remoteAddr != nil && addr != "") {
		raddr, err := net.ResolveUDPAddr(cn.network, addr)
		if err != nil {
			return err
		}

		cn.raddr = raddr
	}

	conn, err := cn.getConn(cn.raddr)
	if err != nil {
		return err
	}

	cn.laddr = conn.LocalAddr()
	cn.mainAddr = conn.LocalAddr()

	cn.cu.Lock()
	cn.conn = conn
	cn.cu.Unlock()

	cn.bu.Lock()
	cn.bw = bufio.NewWriterSize(conn, mnet.MaxBufferSize)
	cn.bu.Unlock()

	cn.waiter.Add(1)
	go cn.readLoop(conn)
	return nil
}

func (cn *clientConn) readLoop(conn *net.UDPConn) {
	defer cn.close()
	defer cn.waiter.Done()

	var tmpReader io.Reader
	incoming := make([]byte, mnet.MinBufferSize)
	for {
		select {
		case <-cn.ctx.Done():
			return
		default:
			nn, addr, err := conn.ReadFrom(incoming)
			if err != nil {
				cn.metrics.Send(metrics.Entry{
					ID:      cn.id,
					Message: "failed to read message from connection",
					Level:   metrics.ErrorLvl,
					Field: metrics.Field{
						"err":  err,
						"addr": addr,
					},
				})

				continue
			}

			cn.metrics.Send(metrics.Entry{
				ID:      cn.id,
				Message: "Received new message",
				Level:   metrics.InfoLvl,
				Field: metrics.Field{
					"addr":   addr,
					"length": nn,
					"data":   string(incoming[:nn]),
				},
			})

			atomic.AddInt64(&cn.totalRead, int64(nn))
			tmpReader = bytes.NewReader(incoming[:nn])
			if err := cn.readIncoming(tmpReader); err != nil {
				cn.metrics.Send(metrics.Entry{
					ID:      cn.id,
					Message: "client unable to handle message",
					Level:   metrics.ErrorLvl,
					Field: metrics.Field{
						"err":    err,
						"addr":   addr,
						"length": nn,
						"data":   string(incoming[:nn]),
					},
				})
				continue
			}

			// Lets resize buffer within area.
			if nn == len(incoming) && nn < mnet.MaxBufferSize {
				incoming = incoming[0 : mnet.MinBufferSize*2]
			}

			if nn < len(incoming)/2 && len(incoming) > mnet.MinBufferSize {
				incoming = incoming[0 : len(incoming)/2]
			}

			if nn > len(incoming) && len(incoming) > mnet.MinBufferSize && nn < mnet.MaxBufferSize {
				incoming = incoming[0 : mnet.MaxBufferSize/2]
			}
		}
	}
}

// getConn returns net.Conn for giving addr.
func (cn *clientConn) getConn(addr net.Addr) (*net.UDPConn, error) {
	lastSleep := mnet.MinTemporarySleep

	var err error
	var conn net.Conn
	for {
		conn, err = cn.dialer.Dial(cn.network, addr.String())
		if err != nil {
			cn.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(cn.id),
				metrics.With("addr", addr),
				metrics.With("network", cn.nid),
				metrics.With("type", "udp"),
				metrics.Message("Connection: failed to connect"),
			)
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				if lastSleep >= mnet.MaxTemporarySleep {
					return nil, err
				}

				time.Sleep(lastSleep)
				lastSleep *= 2
			}
			continue
		}
		break
	}

	if err != nil {
		return nil, err
	}

	if udpcon, ok := conn.(*net.UDPConn); ok {
		if cn.readBuffer != 0 {
			return udpcon, udpcon.SetReadBuffer(cn.readBuffer)
		}

		return udpcon, nil
	}

	return nil, ErrExpectedUDPConn
}
