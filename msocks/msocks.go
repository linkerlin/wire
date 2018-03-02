package msocks

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"context"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/gokit/history"
	"github.com/influx6/faux/netutils"
	"github.com/wirekit/wire"
	"github.com/wirekit/wire/internal"
	uuid "github.com/satori/go.uuid"
)

var (
	wsReadBuffer  = 1024
	wsWriteBuffer = 1024
	wsClientState = ws.StateClientSide
)

// errors ...
var (
	ErrNoTLSConfig = errors.New("no tls.Config provided")
)

// ConnectOptions defines a function type used to apply given
// changes to a *clientNetwork type
type ConnectOptions func(conn *socketClient)

// MaxBuffer sets the clientNetwork to use the provided value
// as its maximum buffer size for it's writer.
func MaxBuffer(buffer int) ConnectOptions {
	return func(cm *socketClient) {
		cm.maxWrite = buffer
	}
}

// TLSConfig sets the giving tls.Config to be used by the returned
// client.
func TLSConfig(config *tls.Config) ConnectOptions {
	return func(cm *socketClient) {
		if config != nil {
			cm.secure = true
			cm.tls = config
		}
	}
}

// KeepAliveTimeout sets the client to use given timeout for it's connection net.Dialer
// keepAliveTimeout.
func KeepAliveTimeout(dur time.Duration) ConnectOptions {
	return func(cm *socketClient) {
		cm.keepTimeout = dur
	}
}

// Dialer sets the ws.Dialer to used creating a connection
// to the server.
func Dialer(dialer *ws.Dialer) ConnectOptions {
	return func(cm *socketClient) {
		cm.dialer = dialer
	}
}

// DialTimeout sets the client to use given timeout for it's connection net.Dialer
// dial timeout.
func DialTimeout(dur time.Duration) ConnectOptions {
	return func(cm *socketClient) {
		cm.dialTimeout = dur
	}
}

// NetworkID sets the id used by the client connection for identifying the
// associated network.
func NetworkID(id string) ConnectOptions {
	return func(cm *socketClient) {
		cm.nid = id
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
	host, _, _ := net.SplitHostPort(addr)

	network := new(socketClient)
	network.websocketServerClient = new(websocketServerClient)

	for _, op := range ops {
		op(network)
	}

	network.id = c.ID
	network.addr = addr
	network.hostname = host

	if network.nid == "" {
		network.nid = "no-network-id"
	}

	if network.network == "" {
		network.network = "udp"
	}

	if network.maxWrite <= 0 {
		network.maxWrite = mnet.MaxBufferSize
	}

	if network.dialer == nil {
		network.dialer = &ws.Dialer{
			Timeout:         network.dialTimeout,
			ReadBufferSize:  wsReadBuffer,
			WriteBufferSize: wsWriteBuffer,
		}
	}

	network.logs = history.WithTags("websocket-client").With("id", network.id)
	network.parser = new(internal.TaggedMessages)

	c.NID = network.nid
	c.CloseFunc = network.close
	c.WriteFunc = network.write
	c.FlushFunc = network.flush
	c.LiveFunc = network.isAlive
	c.LiveFunc = network.isAlive
	c.InfoFunc = network.getInfo
	c.ReaderFunc = network.clientRead
	c.HasPendingFunc = network.hasPending
	c.StatisticFunc = network.getStatistics
	c.LocalAddrFunc = network.getLocalAddr
	c.RemoteAddrFunc = network.getRemoteAddr
	c.ReconnectionFunc = network.reconnect

	if err := network.reconnect(addr); err != nil {
		return c, err
	}

	return c, nil
}

type socketClient struct {
	*websocketServerClient
	addr        string
	hostname    string
	secure      bool
	tls         *tls.Config
	network     string
	dialer      *ws.Dialer
	keepTimeout time.Duration
	dialTimeout time.Duration
	started     int64
}

func (cn *socketClient) isStarted() bool {
	return atomic.LoadInt64(&cn.started) == 1
}

func (cn *socketClient) getInfo() mnet.Info {
	addr := cn.addr
	if cn.remoteAddr != nil {
		addr = cn.remoteAddr.String()
	}

	return mnet.Info{
		ID:         cn.id,
		ServerAddr: addr,
		MinBuffer:  mnet.MinBufferSize,
		MaxBuffer:  int64(cn.maxWrite),
	}
}

func (cn *socketClient) sendRescue() error {
	wc, err := cn.write(len(rescueBytes))
	if err != nil {
		return err
	}

	wc.Write(rescueBytes)
	wc.Close()
	return cn.flush()
}

func (cn *socketClient) handleCINFO() error {
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

func (cn *socketClient) respondToINFO(conn net.Conn, reader io.Reader) error {
	bctx := history.WithTags("websocket-client").WithTitle("socketClient.respondToINFO")

	bctx.Info("Awaiting CINFO request from server")
	bctx.With("client", cn.id).With("network", cn.nid)
	bctx.With("local-addr", cn.localAddr).With("remote-addr", cn.remoteAddr).With("server-addr", cn.serverAddr)

	lreader := internal.NewLengthRecvReader(reader, mnet.HeaderLength)
	msg := make([]byte, mnet.SmallestMinBufferSize)

	var attempts int
	var sendRescueMsg bool

	for {
		// if we failed and timed out, then send rescue message and re-await.
		if sendRescueMsg {
			if err := cn.sendRescue(); err != nil {
				bctx.Red("failed to receive CRESCUE req")
				return err
			}

			sendRescueMsg = false
			time.Sleep(mnet.InfoTemporarySleep)
			continue
		}

		conn.SetReadDeadline(time.Now().Add(mnet.MaxReadDeadline))
		size, err := lreader.ReadHeader()
		if err != nil {
			conn.SetReadDeadline(time.Time{})
			bctx.Red("failed to receive data header")

			// if its a timeout error then retry if we are not maxed attempts.
			if netErr, ok := err.(net.Error); ok {
				if netErr.Timeout() && attempts < mnet.MaxHandshakeAttempts {
					attempts++
					sendRescueMsg = true
					time.Sleep(mnet.InfoTemporarySleep)
					continue
				}
			}

			return err
		}
		conn.SetReadDeadline(time.Time{})

		msg = make([]byte, size)

		_, err = lreader.Read(msg)
		if err != nil {
			bctx.Red("failed to receive mnet.RCINFO req header")
			return err
		}

		if !bytes.Equal(msg, cinfoBytes) {
			bctx.Error(mnet.ErrFailedToRecieveInfo, "no reception of info")
			bctx.Red("invalid mnet.RCINFO prefix")
			return mnet.ErrFailedToRecieveInfo
		}

		break
	}

	bctx.Info("Sending CINFO completed")

	if err := cn.handleCINFO(); err != nil {
		return err
	}

	bctx.Info("Awaiting Handshake complete signal")

	for {
		conn.SetReadDeadline(time.Now().Add(mnet.MaxReadDeadline))
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
			bctx.Red("Failed to read data header")
			return err
		}

		if !bytes.Equal(msg, clientHandshakeCompletedBytes) {
			bctx.Red("handshake not completed")
			return mnet.ErrFailedToCompleteHandshake
		}

		break
	}

	bctx.Info("handshake completed")
	return nil
}

func (cn *socketClient) close() error {
	if err := cn.isAlive(); err != nil {
		return mnet.ErrAlreadyClosed
	}

	err := cn.websocketServerClient.close()
	cn.waiter.Wait()
	return err
}

func (cn *socketClient) reconnect(addr string) error {
	if err := cn.isAlive(); err == nil && cn.isStarted() {
		return nil
	}

	if !strings.HasPrefix(addr, "ws://") && !strings.HasPrefix(addr, "wss://") {
		addr = "ws://" + addr
	}

	if strings.HasPrefix(addr, "wss://") && cn.tls == nil {
		return ErrNoTLSConfig
	}

	defer atomic.StoreInt64(&cn.started, 1)

	cn.waiter.Wait()

	var conn net.Conn
	var err error

	if addr != "" {
		if conn, err = cn.getConn(addr); err != nil {
			conn, err = cn.getConn(cn.addr)
		}
	} else {
		conn, err = cn.getConn(cn.addr)
	}

	if err != nil {
		return err
	}

	cn.localAddr = conn.LocalAddr()
	cn.remoteAddr = conn.RemoteAddr()
	cn.serverAddr = conn.RemoteAddr()

	reader := wsutil.NewReader(conn, wsClientState)
	writer := wsutil.NewWriter(conn, wsClientState, ws.OpBinary)

	cn.cu.Lock()
	cn.conn = conn
	cn.cu.Unlock()

	cn.bu.Lock()
	cn.wsReader = reader
	cn.wsWriter = writer
	cn.bu.Unlock()

	if err := cn.respondToINFO(conn, reader); err != nil {
		return err
	}

	cn.waiter.Add(1)
	go cn.readLoop(conn, reader)

	return nil
}

// getConn returns net.Conn for giving addr.
func (cn *socketClient) getConn(addr string) (net.Conn, error) {
	bctx := cn.logs.WithFields(history.Attrs{
		"client-id": cn.id,
		"network":   cn.nid,
		"protocol":  "tcp",
	})

	var err error
	var conn net.Conn
	var hs ws.Handshake

	lastSleep := mnet.MinTemporarySleep

	for {
		conn, _, hs, err = cn.dialer.Dial(context.Background(), addr)
		if err != nil {
			bctx.Error(err, "Failed to connect to server")

			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				if lastSleep >= mnet.MaxTemporarySleep {
					bctx.Red("Failed to connect to server with expired retries").With("err", err)
					return nil, err
				}

				time.Sleep(lastSleep)
				lastSleep *= 2
			}
			continue
		}
		break
	}

	cn.wshandshake = hs
	return conn, err
}
