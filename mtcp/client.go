package mtcp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"encoding/json"

	"github.com/gokit/history"
	"github.com/influx6/faux/netutils"
	uuid "github.com/satori/go.uuid"
	"github.com/wirekit/llio"
	"github.com/wirekit/wire"
	"github.com/wirekit/wire/internal"
)

// ConnectOptions defines a function type used to apply given
// changes to a *clientNetwork type.
type ConnectOptions func(*clientNetwork)

// TLSConfig sets the giving tls.Config to be used by the returned
// client.
func TLSConfig(config *tls.Config) ConnectOptions {
	return func(cm *clientNetwork) {
		if config != nil {
			cm.secure = true
			cm.tls = config
		}
	}
}

// SecureConnection sets the clientNetwork to use a tls.Connection
// regardless whether certificate is provided.
func SecureConnection() ConnectOptions {
	return func(cm *clientNetwork) {
		cm.secure = true
	}
}

// MaxBuffer sets the clientNetwork to use the provided value
// as its maximum buffer size for it's writer.
func MaxBuffer(buffer int) ConnectOptions {
	return func(cm *clientNetwork) {
		cm.maxWrite = buffer
	}
}

// KeepAliveTimeout sets the client to use given timeout for it's connection net.Dialer
// keepAliveTimeout.
func KeepAliveTimeout(dur time.Duration) ConnectOptions {
	return func(cm *clientNetwork) {
		cm.keepAliveTimeout = dur
	}
}

// DialTimeout sets the client to use given timeout for it's connection net.Dialer
// dial timeout.
func DialTimeout(dur time.Duration) ConnectOptions {
	return func(cm *clientNetwork) {
		cm.dialTimeout = dur
	}
}

// NetworkID sets the id used by the client connection for identifying the
// associated network.
func NetworkID(id string) ConnectOptions {
	return func(cm *clientNetwork) {
		cm.nid = id
	}
}

// Connect is used to implement the client connection to connect to a
// mtcp.Network. It implements all the method functions required
// by the Client to communicate with the server. It understands
// the message length header sent along by every message and follows
// suite when sending to server.
func Connect(addr string, ops ...ConnectOptions) (wire.Client, error) {
	var c wire.Client
	c.ID = uuid.NewV4().String()

	addr = netutils.GetAddr(addr)
	host, _, _ := net.SplitHostPort(addr)

	network := new(clientNetwork)
	for _, op := range ops {
		op(network)
	}

	c.NID = network.nid

	network.totalReconnects = -1
	if network.tls != nil && !network.tls.InsecureSkipVerify {
		network.tls.ServerName = host
	}

	if network.dialTimeout <= 0 {
		network.dialTimeout = wire.DefaultDialTimeout
	}

	if network.keepAliveTimeout <= 0 {
		network.keepAliveTimeout = wire.DefaultKeepAlive
	}

	if network.maxWrite <= 0 {
		network.maxWrite = wire.MaxBufferSize
	}

	if network.dialer == nil {
		network.dialer = &net.Dialer{
			Timeout:   network.dialTimeout,
			KeepAlive: network.keepAliveTimeout,
		}
	}

	network.id = c.ID
	network.addr = addr
	network.parser = new(internal.TaggedMessages)
	network.logs = history.WithTags("mtcp-client").With("id", c.ID).WithFields(map[string]interface{}{
		"addr": addr,
	})

	c.LiveFunc = network.isLive
	c.FlushFunc = network.flush
	c.ReaderFunc = network.read
	c.WriteFunc = network.write
	c.CloseFunc = network.close
	c.BufferedFunc = network.buffered
	c.BroadCastFunc = network.broadcast
	c.HasPendingFunc = network.hasPending
	c.LocalAddrFunc = network.getLocalAddr
	c.RemoteAddrFunc = network.getRemoteAddr
	c.ReconnectionFunc = network.reconnect
	c.StatisticFunc = network.getStatistics

	if err := network.reconnect(addr); err != nil {
		return c, err
	}

	return c, nil
}

type clientNetwork struct {
	totalRead       int64
	totalWritten    int64
	totalFlushed    int64
	MessageRead     int64
	MessageWritten  int64
	totalReconnects int64
	totalMisses     int64
	closed          int64

	dialTimeout      time.Duration
	keepAliveTimeout time.Duration

	maxWrite int
	logs     history.Ctx
	parser   *internal.TaggedMessages

	bu         sync.Mutex
	buffWriter *bufio.Writer

	secure     bool
	id         string
	nid        string
	addr       string
	localAddr  net.Addr
	remoteAddr net.Addr
	do         sync.Once
	tls        *tls.Config
	worker     sync.WaitGroup
	dialer     *net.Dialer

	cu   sync.RWMutex
	conn net.Conn
}

func (cn *clientNetwork) handleCINFO() error {
	jsn, err := json.Marshal(cn.getInfo())
	if err != nil {
		return err
	}

	wc, err := cn.write(len(jsn) + len(rinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(rinfoBytes)
	wc.Write(jsn)
	wc.Close()
	return cn.flush()
}

func (cn *clientNetwork) sendRescue() error {
	wc, err := cn.write(len(rescueBytes))
	if err != nil {
		return err
	}

	wc.Write(rescueBytes)
	wc.Close()
	return cn.flush()
}

func (cn *clientNetwork) respondToINFO(conn net.Conn, reader io.Reader) error {
	lreader := llio.NewLengthRecvReader(reader, wire.HeaderLength)
	msg := make([]byte, wire.SmallestMinBufferSize)

	var attempts int
	var sendRescueMsg bool

	for {
		// if we failed and timed out, then send rescue message and re-await.
		if sendRescueMsg {
			if err := cn.sendRescue(); err != nil {
				return err
			}

			sendRescueMsg = false
			time.Sleep(wire.InfoTemporarySleep)
			continue
		}

		conn.SetReadDeadline(time.Now().Add(wire.MaxReadDeadline))
		size, err := lreader.ReadHeader()
		if err != nil {
			conn.SetReadDeadline(time.Time{})

			// if its a timeout error then retry if we are not maxed attempts.
			if netErr, ok := err.(net.Error); ok {
				if netErr.Timeout() && attempts < wire.MaxHandshakeAttempts {
					attempts++
					sendRescueMsg = true
					time.Sleep(wire.InfoTemporarySleep)
					continue
				}
			}

			return err
		}
		conn.SetReadDeadline(time.Time{})

		msg = make([]byte, size)

		_, err = lreader.Read(msg)
		if err != nil {
			return err
		}

		if !bytes.Equal(msg, cinfoBytes) {
			return wire.ErrFailedToRecieveInfo
		}

		break
	}

	if err := cn.handleCINFO(); err != nil {
		return err
	}

	for {
		conn.SetReadDeadline(time.Now().Add(wire.MaxReadDeadline))
		size, err := lreader.ReadHeader()
		if err != nil {
			conn.SetReadDeadline(time.Time{})
			if err == io.EOF {
				continue
			}
			return err
		}
		conn.SetReadDeadline(time.Time{})

		msg = make([]byte, size)
		_, err = lreader.Read(msg)
		if err != nil {
			return err
		}

		if !bytes.Equal(msg, clientHandshakeCompletedBytes) {
			return wire.ErrFailedToCompleteHandshake
		}

		break
	}

	return nil
}

func (cn *clientNetwork) getInfo() wire.Info {
	addr := cn.addr
	if cn.remoteAddr != nil {
		addr = cn.remoteAddr.String()
	}

	return wire.Info{
		ID:         cn.id,
		ServerAddr: addr,
		MinBuffer:  wire.MinBufferSize,
		MaxBuffer:  int64(cn.maxWrite),
	}
}

func (cn *clientNetwork) getStatistics() (wire.ClientStatistic, error) {
	var stats wire.ClientStatistic
	stats.ID = cn.id
	stats.Local = cn.localAddr
	stats.Remote = cn.remoteAddr
	stats.BytesRead = atomic.LoadInt64(&cn.totalRead)
	stats.MessagesRead = atomic.LoadInt64(&cn.MessageRead)
	stats.BytesWritten = atomic.LoadInt64(&cn.totalWritten)
	stats.BytesFlushed = atomic.LoadInt64(&cn.totalFlushed)
	stats.Reconnects = atomic.LoadInt64(&cn.totalReconnects)
	stats.MessagesWritten = atomic.LoadInt64(&cn.MessageWritten)
	return stats, nil
}

// isLive returns error if clientNetwork is not connected to remote network.
func (cn *clientNetwork) isLive() error {
	if atomic.LoadInt64(&cn.closed) == 1 {
		return wire.ErrAlreadyClosed
	}
	return nil
}

func (cn *clientNetwork) getRemoteAddr() (net.Addr, error) {
	cn.cu.RLock()
	defer cn.cu.RUnlock()
	return cn.remoteAddr, nil
}

func (cn *clientNetwork) getLocalAddr() (net.Addr, error) {
	cn.cu.RLock()
	defer cn.cu.RUnlock()
	return cn.localAddr, nil
}

func (cn *clientNetwork) hasPending() bool {
	if err := cn.isLive(); err != nil {
		return false
	}

	cn.bu.Lock()
	defer cn.bu.Unlock()
	if cn.buffWriter == nil {
		return false
	}

	return cn.buffWriter.Buffered() > 0
}

func (cn *clientNetwork) buffered() (float64, error) {
	if err := cn.isLive(); err != nil {
		return 0, err
	}

	cn.bu.Lock()
	defer cn.bu.Unlock()
	if cn.buffWriter == nil {
		return 0, wire.ErrAlreadyClosed
	}

	available := float64(cn.buffWriter.Available())
	buffered := float64(cn.buffWriter.Buffered())
	return buffered / available, nil
}

func (cn *clientNetwork) flush() error {
	if err := cn.isLive(); err != nil {
		return err
	}

	var conn net.Conn
	cn.cu.RLock()
	conn = cn.conn
	cn.cu.RUnlock()

	if conn == nil {
		return wire.ErrAlreadyClosed
	}

	cn.bu.Lock()
	defer cn.bu.Unlock()
	if cn.buffWriter == nil {
		return wire.ErrAlreadyClosed
	}

	available := cn.buffWriter.Buffered()
	atomic.StoreInt64(&cn.totalFlushed, int64(available))

	conn.SetWriteDeadline(time.Now().Add(wire.MaxFlushDeadline))
	err := cn.buffWriter.Flush()
	if err != nil {
		conn.SetWriteDeadline(time.Time{})
		return err
	}
	conn.SetWriteDeadline(time.Time{})

	return nil
}

func (cn *clientNetwork) broadcast(inSize int) (io.WriteCloser, error) {
	writer, err := cn.write(inSize + len(wire.BROADCAST))
	if err != nil {
		return nil, err
	}

	// write the broadcast header.
	writer.Write(broadcastBytes)
	return writer, nil
}

func (cn *clientNetwork) write(inSize int) (io.WriteCloser, error) {
	if err := cn.isLive(); err != nil {
		return nil, err
	}

	var conn net.Conn
	cn.cu.RLock()
	conn = cn.conn
	cn.cu.RUnlock()

	if conn == nil {
		return nil, wire.ErrAlreadyClosed
	}

	return llio.NewActionLengthWriter(func(size []byte, data []byte) error {
		atomic.AddInt64(&cn.MessageWritten, 1)
		atomic.AddInt64(&cn.totalWritten, int64(len(data)))

		cn.bu.Lock()
		defer cn.bu.Unlock()

		if cn.buffWriter == nil {
			return wire.ErrAlreadyClosed
		}

		//available := cn.buffWriter.Available()
		buffered := cn.buffWriter.Buffered()
		atomic.AddInt64(&cn.totalFlushed, int64(buffered))

		// size of next write.
		toWrite := buffered + len(data)

		// add size header
		toWrite += wire.HeaderLength

		if toWrite >= cn.maxWrite {
			conn.SetWriteDeadline(time.Now().Add(wire.MaxFlushDeadline))
			if err := cn.buffWriter.Flush(); err != nil {
				conn.SetWriteDeadline(time.Time{})
				return err
			}
			conn.SetWriteDeadline(time.Time{})
		}

		if _, err := cn.buffWriter.Write(size); err != nil {
			return err
		}

		if _, err := cn.buffWriter.Write(data); err != nil {
			return err
		}

		return nil
	}, wire.HeaderLength, inSize), nil
}

func (cn *clientNetwork) read() ([]byte, error) {
	if err := cn.isLive(); err != nil {
		return nil, err
	}

	atomic.AddInt64(&cn.MessageRead, 1)
	indata, err := cn.parser.Next()
	atomic.AddInt64(&cn.totalRead, int64(len(indata)))
	if err != nil {
		return nil, err
	}

	// if its a broadcast, just trim the header out.
	if bytes.HasPrefix(indata, broadcastBytes) {
		return bytes.TrimPrefix(indata, broadcastBytes), nil
	}

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := cn.handleCINFO(); err != nil {
			return nil, err
		}
		return nil, wire.ErrNoDataYet
	}

	return indata, nil
}

func (cn *clientNetwork) readLoop(conn net.Conn) {
	defer cn.close()
	defer cn.worker.Done()

	connReader := bufio.NewReaderSize(conn, cn.maxWrite)
	lreader := llio.NewLengthRecvReader(connReader, wire.HeaderLength)

	var incoming []byte

	for {
		frame, err := lreader.ReadHeader()
		if err != nil {
			cn.logs.Error(err, "read header error")
			return
		}

		incoming = make([]byte, frame)
		n, err := lreader.Read(incoming)
		if err != nil {
			cn.logs.Error(err, "read body error")
			return
		}

		datalog := cn.logs.With("data", string(incoming)).Info("client received data")

		atomic.AddInt64(&cn.totalRead, int64(len(incoming[:n])))

		// Send into go-routine (critical path)?
		if err := cn.parser.Parse(incoming[:n]); err != nil {
			datalog.Error(err, "read parser")
			return
		}
	}
}

func (cn *clientNetwork) close() error {
	if err := cn.isLive(); err != nil {
		return err
	}

	cn.flush()

	cn.cu.Lock()
	if cn.conn == nil {
		return wire.ErrAlreadyClosed
	}
	cn.conn.Close()
	cn.cu.Unlock()

	atomic.StoreInt64(&cn.closed, 1)

	cn.worker.Wait()

	cn.cu.Lock()
	cn.conn = nil
	cn.cu.Unlock()

	cn.bu.Lock()
	cn.buffWriter = nil
	cn.bu.Unlock()

	return nil
}

func (cn *clientNetwork) reconnect(altAddr string) error {
	if err := cn.isLive(); err != nil {
		return err
	}

	atomic.AddInt64(&cn.totalReconnects, 1)

	// ensure we really have stopped loop.
	cn.worker.Wait()

	var err error
	var conn net.Conn

	// First we test out the alternate address, to see if we get a connection.
	// If we get no connection, then attempt to dial original address and finally
	// return error.
	if altAddr != "" {
		if conn, err = cn.getConn(altAddr); err != nil {
			conn, err = cn.getConn(cn.addr)
		}
	} else {
		conn, err = cn.getConn(cn.addr)
	}

	// If failure was met, then return error and go-offline again.
	if err != nil {
		return err
	}

	atomic.StoreInt64(&cn.closed, 0)

	cn.bu.Lock()
	cn.buffWriter = bufio.NewWriterSize(conn, cn.maxWrite)
	cn.bu.Unlock()

	cn.bu.Lock()
	cn.buffWriter.Reset(conn)
	cn.bu.Unlock()

	cn.cu.Lock()
	cn.do = sync.Once{}
	cn.conn = conn
	cn.localAddr = conn.LocalAddr()
	cn.remoteAddr = conn.RemoteAddr()
	cn.cu.Unlock()

	cn.logs.With("local-addr", cn.localAddr)
	cn.logs.With("remote-addr", cn.remoteAddr)
	cn.logs.With("addr", cn.remoteAddr.String())

	connReader := bufio.NewReaderSize(conn, wire.MaxBufferSize)
	if err := cn.respondToINFO(conn, connReader); err != nil {
		return err
	}

	cn.worker.Add(1)
	go cn.readLoop(conn)

	return nil
}

// getConn returns net.Conn for giving addr.
func (cn *clientNetwork) getConn(addr string) (net.Conn, error) {
	lastSleep := wire.MinTemporarySleep

	var err error
	var conn net.Conn

	for {
		conn, err = cn.dialer.Dial("tcp", addr)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				if lastSleep >= wire.MaxTemporarySleep {
					return nil, err
				}

				time.Sleep(lastSleep)
				lastSleep *= 2
			}
			continue
		}
		break
	}

	if cn.secure && cn.tls != nil {
		tlsConn := tls.Client(conn, cn.tls)
		if err := tlsConn.Handshake(); err != nil {
			return nil, err
		}

		return tlsConn, nil
	}

	if cn.secure && cn.tls == nil {
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
		if err := tlsConn.Handshake(); err != nil {
			return nil, err
		}

		return tlsConn, nil
	}

	return conn, nil
}
