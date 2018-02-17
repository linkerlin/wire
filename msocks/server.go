package msocks

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/influx6/faux/metrics"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/melon"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/internal"
	"github.com/influx6/mnet/mlisten"
	uuid "github.com/satori/go.uuid"
)

var (
	wsState                 = ws.StateServerSide
	cinfoBytes              = []byte(mnet.CINFO)
	rinfoBytes              = []byte(mnet.RINFO)
	clStatusBytes           = []byte(mnet.CLSTATUS)
	handshakeCompletedBytes = []byte(mnet.CLHANDSHAKECOMPLETED)
)

//************************************************************************
//  Websocket Server Client Implementation
//************************************************************************

type websocketServerClient struct {
	totalRead      int64
	totalWritten   int64
	totalFlushOut  int64
	totalWriteMsgs int64
	totalReadMsgs  int64
	id             string
	nid            string
	isACluster     bool
	srcInfo        *mnet.Info
	localAddr      net.Addr
	remoteAddr     net.Addr
	maxWrite       int
	serverAddr     net.Addr
	maxDeadline    time.Duration
	maxInfoWait    time.Duration
	wshandshake    ws.Handshake
	metrics        metrics.Metrics
	parser         *internal.TaggedMessages
	closedCounter  int64
	waiter         sync.WaitGroup
	wsReader       *wsutil.Reader
	bu             sync.Mutex
	wsWriter       *wsutil.Writer
	cu             sync.Mutex
	conn           net.Conn
}

// isCluster returns true/false if a giving connection is actually
// connected to another server i.e a server-to-server and not a
// client-to-server connection.
func (sc *websocketServerClient) isCluster(cm mnet.Client) bool {
	return sc.isACluster
}

func (sc *websocketServerClient) getRemoteAddr(_ mnet.Client) (net.Addr, error) {
	return sc.remoteAddr, nil
}

func (sc *websocketServerClient) getLocalAddr(_ mnet.Client) (net.Addr, error) {
	return sc.localAddr, nil
}

func (sc *websocketServerClient) getStatistics(_ mnet.Client) (mnet.ClientStatistic, error) {
	var stats mnet.ClientStatistic
	stats.ID = sc.id
	stats.Local = sc.localAddr
	stats.Remote = sc.remoteAddr
	stats.BytesRead = atomic.LoadInt64(&sc.totalRead)
	stats.BytesFlushed = atomic.LoadInt64(&sc.totalFlushOut)
	stats.BytesWritten = atomic.LoadInt64(&sc.totalWritten)
	stats.MessagesRead = atomic.LoadInt64(&sc.totalReadMsgs)
	stats.MessagesWritten = atomic.LoadInt64(&sc.totalWriteMsgs)
	return stats, nil
}

func (sc *websocketServerClient) write(mn mnet.Client, size int) (io.WriteCloser, error) {
	if err := sc.isAlive(mn); err != nil {
		return nil, err
	}

	var conn net.Conn
	sc.cu.Lock()
	if sc.conn == nil {
		sc.cu.Unlock()
		return nil, mnet.ErrAlreadyClosed
	}
	conn = sc.conn
	sc.cu.Unlock()

	//_ = conn

	return internal.NewActionLengthWriter(func(size []byte, data []byte) error {
		atomic.AddInt64(&sc.totalReadMsgs, 1)
		atomic.AddInt64(&sc.totalWritten, int64(len(data)))

		sc.bu.Lock()
		defer sc.bu.Unlock()

		if sc.wsWriter == nil {
			return mnet.ErrAlreadyClosed
		}

		//available := sc.wsWriter.Available()
		buffered := sc.wsWriter.Buffered()
		atomic.AddInt64(&sc.totalFlushOut, int64(buffered))

		// size of next write.
		toWrite := buffered + len(data)

		// add size header
		toWrite += mnet.HeaderLength

		if toWrite >= sc.maxWrite {
			conn.SetWriteDeadline(time.Now().Add(sc.maxDeadline))
			if err := sc.wsWriter.Flush(); err != nil {
				conn.SetWriteDeadline(time.Time{})
				sc.metrics.Emit(
					metrics.Error(err),
					metrics.WithID(sc.id),
					metrics.With("network", sc.nid),
					metrics.Message("Connection failed to pre-flush existing data before new write"),
				)
				return err
			}
			conn.SetWriteDeadline(time.Time{})
		}

		if _, err := sc.wsWriter.Write(size); err != nil {
			return err
		}

		if _, err := sc.wsWriter.Write(data); err != nil {
			return err
		}

		return nil
	}, mnet.HeaderLength, size), nil
}

func (sc *websocketServerClient) isAlive(_ mnet.Client) error {
	if atomic.LoadInt64(&sc.closedCounter) == 1 {
		return mnet.ErrAlreadyClosed
	}
	return nil
}

func (sc *websocketServerClient) clientRead(mn mnet.Client) ([]byte, error) {
	if err := sc.isAlive(mn); err != nil {
		return nil, err
	}

	atomic.AddInt64(&sc.totalReadMsgs, 1)
	indata, err := sc.parser.Next()
	atomic.AddInt64(&sc.totalRead, int64(len(indata)))
	if err != nil {
		return nil, err
	}

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := sc.handleCINFO(mn); err != nil {
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	return indata, nil
}

// read reads from incoming message handling necessary
// handshake response and requests received over the wire,
// while returning non-hanshake messages.
func (sc *websocketServerClient) serverRead(cm mnet.Client) ([]byte, error) {
	if cerr := sc.isAlive(cm); cerr != nil {
		return nil, cerr
	}

	atomic.AddInt64(&sc.totalReadMsgs, 1)

	indata, err := sc.parser.Next()
	if err != nil {
		return nil, err
	}

	atomic.AddInt64(&sc.totalRead, int64(len(indata)))

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := sc.handleCINFO(cm); err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.With("client", sc.id),
				metrics.With("network", sc.nid),
				metrics.Message("handleCINFO failed"),
			)
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, clStatusBytes) {
		if err := sc.handleCLStatusReceive(cm, indata); err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.With("client", sc.id),
				metrics.With("network", sc.nid),
				metrics.Message("handleCLStatusRecieve failed"),
			)
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	return indata, nil
}

func (sc *websocketServerClient) flush(mn mnet.Client) error {
	if err := sc.isAlive(mn); err != nil {
		return err
	}

	var conn net.Conn
	sc.cu.Lock()
	conn = sc.conn
	sc.cu.Unlock()

	if conn == nil {
		return mnet.ErrAlreadyClosed
	}

	sc.bu.Lock()
	defer sc.bu.Unlock()

	if sc.wsWriter == nil {
		return mnet.ErrAlreadyClosed
	}

	if sc.wsWriter.Buffered() != 0 {
		conn.SetWriteDeadline(time.Now().Add(sc.maxDeadline))
		if err := sc.wsWriter.Flush(); err != nil {
			conn.SetWriteDeadline(time.Time{})
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.Message("Connection failed to flush data"),
				metrics.With("network", sc.nid),
			)
			return err
		}
		conn.SetWriteDeadline(time.Time{})
	}

	return nil
}

func (sc *websocketServerClient) close(mn mnet.Client) error {
	if err := sc.isAlive(mn); err != nil {
		sc.metrics.Emit(
			metrics.Error(err),
			metrics.WithID(sc.id),
			metrics.With("client", sc.id),
			metrics.With("network", sc.nid),
			metrics.Message("Closing websocket node connection"),
		)
		return err
	}

	sc.metrics.Emit(
		metrics.WithID(sc.id),
		metrics.With("client", sc.id),
		metrics.With("network", sc.nid),
		metrics.Message("Closing websocket node connection"),
	)

	atomic.StoreInt64(&sc.closedCounter, 1)

	sc.flush(mn)

	var err error
	sc.cu.Lock()
	if sc.conn != nil {
		err = sc.conn.Close()
	}
	sc.cu.Unlock()

	sc.waiter.Wait()

	sc.bu.Lock()
	sc.wsWriter = nil
	sc.wsReader = nil
	sc.bu.Unlock()

	sc.cu.Lock()
	sc.conn = nil
	sc.cu.Unlock()

	if err != nil {
		sc.metrics.Emit(
			metrics.Error(err),
			metrics.WithID(sc.id),
			metrics.With("client", sc.id),
			metrics.With("network", sc.nid),
			metrics.Message("Closed websocket net.Conn connection"),
		)
	}

	return err
}

// readLoop handles the necessary operation of reading data from the
// underline connection.
func (sc *websocketServerClient) readLoop(conn net.Conn, reader *wsutil.Reader) {
	defer sc.close(mnet.Client{})
	defer sc.waiter.Done()

	lreader := internal.NewLengthRecvReader(reader, mnet.HeaderLength)

	incoming := make([]byte, mnet.SmallestMinBufferSize)

	for {

		wsFrame, err := reader.NextFrame()
		if err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.With("client", sc.id),
				metrics.With("network", sc.nid),
				metrics.Message("Connection failed to read next frame"),
			)
			return
		}

		frame, err := lreader.ReadHeader()
		if err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.With("frame", frame),
				metrics.With("ws-frame", wsFrame),
				metrics.Message("Connection failed to read data frame"),
				metrics.With("network", sc.nid),
			)
			return
		}

		incoming = make([]byte, frame)

		n, err := lreader.Read(incoming)
		if err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.With("read-length", n),
				metrics.With("length", len(incoming[:n])),
				metrics.With("data", string(incoming[:n])),
				metrics.Message("Connection failed to read: closing"),
				metrics.With("network", sc.nid),
			)
			return
		}

		sc.metrics.Send(metrics.Entry{
			Message: "Received websocket message",
			Field: metrics.Field{
				"data":     string(incoming[:n]),
				"length":   len(incoming[:n]),
				"frame":    frame,
				"ws-frame": wsFrame,
			},
		})

		atomic.AddInt64(&sc.totalRead, int64(len(incoming[:n])))

		// Send into go-routine (critical path)?
		if err := sc.parser.Parse(incoming[:n]); err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.Message("ParseError"),
				metrics.With("network", sc.nid),
				metrics.With("length", len(incoming)),
				metrics.With("data", string(incoming)),
			)
			return
		}
	}
}

func (sc *websocketServerClient) getInfo(cm mnet.Client) mnet.Info {
	if sc.isCluster(cm) && sc.srcInfo != nil {
		return *sc.srcInfo
	}

	return mnet.Info{
		ID:         sc.id,
		ServerNode: true,
		Cluster:    sc.isACluster,
		ServerAddr: sc.serverAddr.String(),
		MaxBuffer:  int64(sc.maxWrite),
		MinBuffer:  mnet.MinBufferSize,
	}

}

func (sc *websocketServerClient) handleCLStatusReceive(cm mnet.Client, data []byte) error {
	data = bytes.TrimPrefix(data, clStatusBytes)

	var info mnet.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	sc.srcInfo = &info

	wc, err := sc.write(cm, len(handshakeCompletedBytes))
	if err != nil {
		return err
	}

	wc.Write(handshakeCompletedBytes)
	wc.Close()
	return sc.flush(cm)
}

func (sc *websocketServerClient) handleCLStatusSend(cm mnet.Client) error {
	var info mnet.Info
	info.ID = sc.nid
	info.Cluster = true
	info.ServerNode = true
	info.MinBuffer = mnet.MinBufferSize
	info.MaxBuffer = int64(sc.maxWrite)
	info.ServerAddr = sc.serverAddr.String()

	jsn, err := json.Marshal(info)
	if err != nil {
		return err
	}

	wc, err := sc.write(cm, len(jsn)+len(clStatusBytes))
	if err != nil {
		return err
	}

	wc.Write(clStatusBytes)
	wc.Write(jsn)
	wc.Close()
	return sc.flush(cm)
}

func (sc *websocketServerClient) handleCINFO(cm mnet.Client) error {
	jsn, err := json.Marshal(sc.getInfo(cm))
	if err != nil {
		return err
	}

	wc, err := sc.write(cm, len(jsn)+len(rinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(rinfoBytes)
	wc.Write(jsn)
	wc.Close()
	return sc.flush(cm)
}

func (sc *websocketServerClient) handleRINFO(data []byte) error {

	data = bytes.TrimPrefix(data, rinfoBytes)

	var info mnet.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	info.Cluster = sc.isACluster
	sc.srcInfo = &info
	return nil
}

func (sc *websocketServerClient) handshake(cm mnet.Client) error {
	sc.metrics.Emit(
		metrics.WithID(sc.id),
		metrics.With("client", sc.id),
		metrics.With("network", sc.nid),
		metrics.With("local-addr", sc.localAddr),
		metrics.With("remote-addr", sc.remoteAddr),
		metrics.With("server-addr", sc.serverAddr),
		metrics.Message("websocketServerClient.Handshake"),
	)

	// Send to new client mnet.CINFO request
	wc, err := sc.write(cm, len(cinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(cinfoBytes)
	wc.Close()
	sc.flush(cm)

	before := time.Now()

	sc.metrics.Emit(
		metrics.WithID(sc.id),
		metrics.With("client", sc.id),
		metrics.With("network", sc.nid),
		metrics.With("local-addr", sc.localAddr),
		metrics.With("remote-addr", sc.remoteAddr),
		metrics.With("server-addr", sc.serverAddr),
		metrics.Message("websocketServerClient.Handshake: Awating CINFO req"),
	)

	// Wait for RCINFO response from connection.
	for {
		msg, err := sc.serverRead(cm)
		if err != nil {
			if err != mnet.ErrNoDataYet {
				return err
			}

			//time.Sleep(mnet.InfoTemporarySleep)
			continue
		}

		if time.Now().Sub(before) > sc.maxInfoWait {
			sc.close(cm)
			sc.metrics.Emit(
				metrics.Error(mnet.ErrFailedToRecieveInfo),
				metrics.WithID(sc.id),
				metrics.With("client", sc.id),
				metrics.With("network", sc.nid),
				metrics.Message("Timeout: awaiting mnet.RCINFo data"),
			)
			return mnet.ErrFailedToRecieveInfo
		}

		// First message should be a mnet.RCINFO response.
		if !bytes.HasPrefix(msg, rinfoBytes) {
			sc.close(cm)
			sc.metrics.Emit(
				metrics.Error(mnet.ErrFailedToRecieveInfo),
				metrics.WithID(sc.id),
				metrics.With("client", sc.id),
				metrics.With("network", sc.nid),
				metrics.With("data", string(msg)),
				metrics.Message("Invalid mnet.RCINFO prefix"),
			)
			return mnet.ErrFailedToRecieveInfo
		}

		if err := sc.handleRINFO(msg); err != nil {
			sc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(sc.id),
				metrics.With("client", sc.id),
				metrics.With("network", sc.nid),
				metrics.With("data", string(msg)),
				metrics.Message("Invalid mnet.RCINFO data"),
			)
			return err
		}

		break
	}

	sc.metrics.Emit(
		metrics.WithID(sc.id),
		metrics.With("client", sc.id),
		metrics.With("network", sc.nid),
		metrics.With("local-addr", sc.localAddr),
		metrics.With("remote-addr", sc.remoteAddr),
		metrics.With("server-addr", sc.serverAddr),
		metrics.Message("websocketServerClient.Handshake: Send Cluster Status Message"),
	)

	// if its a cluster send Cluster Status message.
	if sc.isACluster {
		if err := sc.handleCLStatusSend(cm); err != nil {
			return err
		}

		before = time.Now()

		// Wait for handshake completion signal.
		for {
			msg, err := sc.serverRead(cm)
			if err != nil {
				if err != mnet.ErrNoDataYet {
					return err
				}

				//time.Sleep(mnet.InfoTemporarySleep)
				continue
			}

			if time.Now().Sub(before) > sc.maxInfoWait {
				sc.close(cm)
				sc.metrics.Emit(
					metrics.Error(mnet.ErrFailedToCompleteHandshake),
					metrics.WithID(sc.id),
					metrics.With("client", sc.id),
					metrics.With("network", sc.nid),
					metrics.Message("Failed to receive handshake completion before max wait"),
				)
				return mnet.ErrFailedToCompleteHandshake
			}

			if !bytes.Equal(msg, handshakeCompletedBytes) {
				sc.metrics.Emit(
					metrics.Error(mnet.ErrFailedToCompleteHandshake),
					metrics.WithID(sc.id),
					metrics.With("client", sc.id),
					metrics.With("network", sc.nid),
					metrics.With("data", string(msg)),
					metrics.Message("Invalid handshake completion data"),
				)
				return mnet.ErrFailedToCompleteHandshake
			}

			break
		}
	}

	sc.metrics.Emit(
		metrics.WithID(sc.id),
		metrics.With("client", sc.id),
		metrics.With("network", sc.nid),
		metrics.With("local-addr", sc.localAddr),
		metrics.With("remote-addr", sc.remoteAddr),
		metrics.With("server-addr", sc.serverAddr),
		metrics.Message("websocketServerClient.Handshake: Completed"),
	)

	return nil
}

//************************************************************************
//  Websocket Server Implementation
//************************************************************************

// WebsocketNetwork defines a network which runs ontop of provided mnet.ConnHandler.
type WebsocketNetwork struct {
	ID         string
	Addr       string
	ServerName string
	TLS        *tls.Config
	Upgrader   *ws.Upgrader
	Handler    mnet.ConnHandler
	Metrics    metrics.Metrics

	totalClients int64
	totalClosed  int64
	totalActive  int64
	totalOpened  int64
	started      int64

	// Dialer to be used to create net.Conn to connect to cluster address
	// in TCPNetwork.AddCluster. Set to use a custom dialer.
	Dialer *ws.Dialer

	// MaxClusterRetries specifies allowed max retries used by the
	// RetryPolicy for reconnecting to a disconnected cluster network.
	MaxClusterRetries int

	// ClusterRetryDelay defines the duration used to expand for
	// each retry of reconnecting to a cluster address.
	ClusterRetryDelay time.Duration

	// MaxConnection defines the total allowed connections for
	// giving network.
	MaxConnections int

	// MaxWriteDeadline defines max time before all clients collected writes
	// must be written to the connection.
	MaxDeadline time.Duration

	// MaxInfoWait defines the max time a connection awaits the completion of a
	// handshake phase.
	MaxInfoWait time.Duration

	// MaxWriteSize sets given max size of buffer for client, each client
	// writes collected till flush must not exceed else will not be buffered
	// and will be written directly.
	MaxWriteSize int

	raddr    net.Addr
	cu       sync.RWMutex
	clients  map[string]*websocketServerClient
	routines sync.WaitGroup
}

func (n *WebsocketNetwork) isAlive() error {
	if atomic.LoadInt64(&n.started) == 0 {
		return errors.New("not started yet")
	}
	return nil
}

// Start initializes the network listener.
func (n *WebsocketNetwork) Start(ctx context.Context) error {
	if err := n.isAlive(); err == nil {
		return nil
	}

	if n.ID == "" {
		n.ID = uuid.NewV4().String()
	}

	if n.Metrics == nil {
		n.Metrics = metrics.New()
	}

	if n.Upgrader == nil {
		n.Upgrader = &ws.Upgrader{}
	}

	n.Addr = netutils.GetAddr(n.Addr)
	if n.ServerName == "" {
		host, _, _ := net.SplitHostPort(n.Addr)
		n.ServerName = host
	}

	defer n.Metrics.Emit(
		metrics.Message("WebsocketNetwork.Start"),
		metrics.With("network", n.ID),
		metrics.With("addr", n.Addr),
		metrics.With("serverName", n.ServerName),
		metrics.WithID(n.ID),
	)

	if n.TLS != nil && !n.TLS.InsecureSkipVerify {
		n.TLS.ServerName = n.ServerName
	}

	if n.Dialer != nil && n.Dialer.TLSConfig == nil {
		n.Dialer.TLSConfig = n.TLS
	}

	stream, err := mlisten.Listen("tcp", n.Addr, n.TLS)
	if err != nil {
		return err
	}

	n.raddr = stream.Addr()
	n.clients = make(map[string]*websocketServerClient)

	if n.MaxConnections <= 0 {
		n.MaxConnections = mnet.MaxConnections
	}

	if n.MaxClusterRetries <= 0 {
		n.MaxClusterRetries = mnet.MaxReconnectRetries
	}

	if n.ClusterRetryDelay <= 0 {
		n.ClusterRetryDelay = mnet.DefaultClusterRetryDelay
	}

	if n.MaxInfoWait <= 0 {
		n.MaxInfoWait = mnet.MaxInfoWait
	}

	if n.MaxWriteSize <= 0 {
		n.MaxWriteSize = mnet.MaxBufferSize
	}

	if n.MaxDeadline <= 0 {
		n.MaxDeadline = mnet.MaxFlushDeadline
	}

	n.routines.Add(1)
	go n.handleConnections(ctx, stream)
	go func() {
		<-ctx.Done()
		stream.Close()
	}()

	return nil
}

var defaultDialer = &ws.Dialer{
	Timeout:         time.Second * 2,
	ReadBufferSize:  wsReadBuffer,
	WriteBufferSize: wsWriteBuffer,
}

// AddCluster attempts to add new connection to another mnet tcp server.
// It will attempt to dial specified address returning an error if the
// giving address failed or was not able to meet the handshake protocols.
func (n *WebsocketNetwork) AddCluster(addr string) error {
	if !strings.HasPrefix(addr, "ws://") && !strings.HasPrefix(addr, "wss://") {
		if n.TLS == nil {
			addr = "ws://" + addr
		} else {
			addr = "wss://" + addr
		}
	}

	var err error
	var hs ws.Handshake
	var conn net.Conn

	if n.Dialer != nil {
		conn, _, hs, err = n.Dialer.Dial(context.Background(), addr)
	} else {
		// Set the tls config just incase we have one and need to use default dialer.
		defaultDialer.TLSConfig = n.TLS
		conn, _, hs, err = defaultDialer.Dial(context.Background(), addr)
	}

	if err != nil {
		return err
	}

	if n.connectedToMe(conn) {
		conn.Close()
		return mnet.ErrAlreadyServiced
	}

	policy := mnet.FunctionPolicy(n.MaxClusterRetries, func() error {
		return n.AddCluster(addr)
	}, mnet.ExponentialDelay(n.ClusterRetryDelay))

	return n.addWSClient(conn, hs, policy, true)
}

// connectedToMe returns true/false if we are already connected to server.
func (n *WebsocketNetwork) connectedToMe(conn net.Conn) bool {
	n.cu.Lock()
	defer n.cu.Unlock()

	remote := conn.RemoteAddr().String()
	for _, conn := range n.clients {
		if conn.srcInfo == nil {
			continue
		}

		if conn.srcInfo.ServerAddr == remote {
			return true
		}
	}

	return false
}

// AddClient adds a new websocket client through the provided
// net.Conn.
func (n *WebsocketNetwork) addClient(newConn net.Conn, policy mnet.RetryPolicy, isCluster bool) error {
	handshake, err := n.Upgrader.Upgrade(newConn)
	if err != nil {
		n.Metrics.Send(metrics.Entry{
			ID:      n.ID,
			Message: "Failed to upgrade connection",
			Field: metrics.Field{
				"err":         err,
				"remote_addr": newConn.RemoteAddr(),
				"local_addr":  newConn.LocalAddr(),
			},
		})

		return newConn.Close()
	}

	n.Metrics.Send(metrics.Entry{
		ID:      n.ID,
		Message: "Upgraded connection successfully",
		Field: metrics.Field{
			"handshake":   handshake,
			"remote_addr": newConn.RemoteAddr(),
			"local_addr":  newConn.LocalAddr(),
		},
	})

	if err := n.addWSClient(newConn, handshake, policy, isCluster); err != nil {
		newConn.Close()
		return err
	}

	return nil
}

func (n *WebsocketNetwork) addWSClient(conn net.Conn, hs ws.Handshake, policy mnet.RetryPolicy, isCluster bool) error {
	atomic.AddInt64(&n.totalClients, 1)
	atomic.AddInt64(&n.totalOpened, 1)

	var wsReader *wsutil.Reader
	var wsWriter *wsutil.Writer

	if isCluster {
		wsReader = wsutil.NewReader(conn, wsClientState)
		wsWriter = wsutil.NewWriter(conn, wsClientState, ws.OpBinary)
	} else {
		wsReader = wsutil.NewReader(conn, wsState)
		wsWriter = wsutil.NewWriterSize(conn, wsState, ws.OpBinary, n.MaxWriteSize)
	}

	client := new(websocketServerClient)
	client.nid = n.ID
	client.conn = conn
	client.wshandshake = hs
	client.metrics = n.Metrics
	client.wsReader = wsReader
	client.wsWriter = wsWriter
	client.serverAddr = n.raddr
	client.isACluster = isCluster
	client.maxWrite = n.MaxWriteSize
	client.maxInfoWait = n.MaxInfoWait
	client.id = uuid.NewV4().String()
	client.maxDeadline = n.MaxDeadline
	client.localAddr = conn.LocalAddr()
	client.remoteAddr = conn.RemoteAddr()
	client.parser = new(internal.TaggedMessages)

	defer n.Metrics.Emit(
		metrics.Message("WebsocketNetwork.addClient: add new client"),
		metrics.With("network", n.ID),
		metrics.With("addr", n.Addr),
		metrics.With("serverName", n.ServerName),
		metrics.With("client_id", client.id),
		metrics.With("client_addr", conn.RemoteAddr()),
		metrics.WithID(n.ID),
	)

	client.waiter.Add(1)
	go client.readLoop(conn, wsReader)

	var mclient mnet.Client
	mclient.Metrics = n.Metrics
	mclient.NID = n.ID
	mclient.ID = client.id
	mclient.CloseFunc = client.close
	mclient.WriteFunc = client.write
	mclient.FlushFunc = client.flush
	mclient.LiveFunc = client.isAlive
	mclient.LiveFunc = client.isAlive
	mclient.InfoFunc = client.getInfo
	mclient.SiblingsFunc = n.getAllClient
	mclient.ReaderFunc = client.serverRead
	mclient.LocalAddrFunc = client.getLocalAddr
	mclient.StatisticFunc = client.getStatistics
	mclient.RemoteAddrFunc = client.getRemoteAddr

	// Initial handshake protocol.
	if err := client.handshake(mclient); err != nil {
		return err
	}

	n.cu.Lock()
	n.clients[client.id] = client
	n.cu.Unlock()

	n.routines.Add(1)
	go func(mc mnet.Client, addr net.Addr) {
		defer n.routines.Done()
		defer atomic.AddInt64(&n.totalClients, -1)
		defer atomic.AddInt64(&n.totalClosed, 1)
		defer atomic.StoreInt64(&client.closedCounter, 1)

		if err := n.Handler(mc); err != nil {
			n.Metrics.Emit(
				metrics.Error(err),
				metrics.WithID(mclient.ID),
				metrics.Message("Connection handler failed"),
				metrics.With("network", n.ID),
				metrics.With("addr", addr),
			)
		}

		n.cu.Lock()
		delete(n.clients, client.id)
		n.cu.Unlock()

		if policy != nil {
			go func() {
				if err := policy.Retry(); err != nil {
					n.Metrics.Emit(
						metrics.Error(err),
						metrics.With("addr", addr),
						metrics.WithID(mclient.ID),
						metrics.With("network", n.ID),
						metrics.Message("RetryPolicy failed"),
					)
				}
			}()
		}
	}(mclient, client.remoteAddr)

	return nil
}

func (n *WebsocketNetwork) getAllClient(from mnet.Client) ([]mnet.Client, error) {
	n.cu.Lock()
	defer n.cu.Unlock()

	var clients []mnet.Client
	for _, conn := range n.clients {
		if conn.id == from.ID {
			continue
		}

		var client mnet.Client
		client.NID = n.ID
		client.ID = conn.id
		client.Metrics = n.Metrics
		client.LiveFunc = conn.isAlive
		client.InfoFunc = conn.getInfo
		client.WriteFunc = conn.write
		client.FlushFunc = conn.flush
		client.StatisticFunc = conn.getStatistics
		client.RemoteAddrFunc = conn.getRemoteAddr
		client.LocalAddrFunc = conn.getLocalAddr
		client.SiblingsFunc = n.getAllClient
		clients = append(clients, client)
	}

	return clients, nil
}

// handleConnections runs the process of listening for new connections and
// creating appropriate client objects which will handle behaviours
// appropriately.
func (n *WebsocketNetwork) handleConnections(ctx context.Context, stream melon.ConnReadWriteCloser) {
	defer n.routines.Done()
	defer n.closeClientConnections(ctx)

	defer n.Metrics.Emit(
		metrics.With("network", n.ID),
		metrics.Message("WebsocketNetwork.runStream"),
		metrics.With("addr", n.Addr),
		metrics.With("serverName", n.ServerName),
		metrics.WithID(n.ID),
	)

	initial := mnet.MinTemporarySleep

	for {
		newConn, err := stream.ReadConn()
		if err != nil {
			n.Metrics.Send(metrics.Entry{
				ID:      n.ID,
				Field:   metrics.Field{"err": err},
				Message: "Failed to read connection",
			})

			if err == mlisten.ErrListenerClosed {
				return
			}

			netErr, ok := err.(net.Error)
			if !ok {
				continue
			}

			if netErr.Temporary() {
				time.Sleep(initial)
				initial *= 2

				if initial >= mnet.MaxTemporarySleep {
					initial = mnet.MinTemporarySleep
				}
			}

			continue
		}

		if int(atomic.LoadInt64(&n.totalOpened)) >= n.MaxConnections {
			newConn.Close()
			continue
		}

		n.addClient(newConn, nil, false)
	}
}

func (n *WebsocketNetwork) closeClientConnections(ctx context.Context) {
	n.cu.RLock()
	defer n.cu.RUnlock()
	for _, conn := range n.clients {
		conn.close(mnet.Client{})
	}
}

// Statistics returns statics associated with WebsocketNetwork.
func (n *WebsocketNetwork) Statistics() mnet.NetworkStatistic {
	var stats mnet.NetworkStatistic
	stats.ID = n.ID
	stats.LocalAddr = n.raddr
	stats.RemoteAddr = n.raddr
	stats.TotalClients = atomic.LoadInt64(&n.totalClients)
	stats.TotalClosed = atomic.LoadInt64(&n.totalClosed)
	stats.TotalOpened = atomic.LoadInt64(&n.totalOpened)
	return stats
}

// Wait is called to ensure network ended.
func (n *WebsocketNetwork) Wait() {
	n.routines.Wait()
}
