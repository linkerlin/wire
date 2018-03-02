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
	"github.com/gokit/history"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/melon"
	"github.com/wirekit/wire"
	"github.com/wirekit/wire/internal"
	"github.com/wirekit/wire/mlisten"
	uuid "github.com/satori/go.uuid"
)

var (
	wsState                       = ws.StateServerSide
	cinfoBytes                    = []byte(mnet.CINFO)
	rinfoBytes                    = []byte(mnet.RINFO)
	clStatusBytes                 = []byte(mnet.CLSTATUS)
	rescueBytes                   = []byte(mnet.CRESCUE)
	handshakeCompletedBytes       = []byte(mnet.CLHANDSHAKECOMPLETED)
	clientHandshakeCompletedBytes = []byte(mnet.ClientHandShakeCompleted)
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
	logs           history.Ctx
	srcInfo        *mnet.Info
	localAddr      net.Addr
	remoteAddr     net.Addr
	maxWrite       int
	serverAddr     net.Addr
	wshandshake    ws.Handshake
	network        *WebsocketNetwork
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
func (sc *websocketServerClient) isCluster() bool {
	return sc.isACluster
}

func (sc *websocketServerClient) getRemoteAddr() (net.Addr, error) {
	return sc.remoteAddr, nil
}

func (sc *websocketServerClient) getLocalAddr() (net.Addr, error) {
	return sc.localAddr, nil
}

func (sc *websocketServerClient) getStatistics() (mnet.ClientStatistic, error) {
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

func (sc *websocketServerClient) write(size int) (io.WriteCloser, error) {
	if err := sc.isAlive(); err != nil {
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
			conn.SetWriteDeadline(time.Now().Add(mnet.MaxFlushDeadline))
			if err := sc.wsWriter.Flush(); err != nil {
				conn.SetWriteDeadline(time.Time{})
				history.WithTags("websocket-server").WithTitle("websocketServerClient.write").Error(err, "flush failed").Red("failed to pre-flush data")
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

func (sc *websocketServerClient) isAlive() error {
	if atomic.LoadInt64(&sc.closedCounter) == 1 {
		return mnet.ErrAlreadyClosed
	}
	return nil
}

func (sc *websocketServerClient) clientRead() ([]byte, error) {
	if err := sc.isAlive(); err != nil {
		return nil, err
	}

	atomic.AddInt64(&sc.totalReadMsgs, 1)
	indata, err := sc.parser.Next()
	atomic.AddInt64(&sc.totalRead, int64(len(indata)))
	if err != nil {
		return nil, err
	}

	//if bytes.HasPrefix(indata, clientHandshakeCompletedBytes) {
	//	return nil, mnet.ErrNoDataYet
	//}

	if bytes.Equal(indata, rescueBytes) {
		return nil, mnet.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := sc.handleCINFO(); err != nil {
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	return indata, nil
}

// read reads from incoming message handling necessary
// handshake response and requests received over the wire,
// while returning non-hanshake messages.
func (sc *websocketServerClient) serverRead() ([]byte, error) {
	if cerr := sc.isAlive(); cerr != nil {
		return nil, cerr
	}

	atomic.AddInt64(&sc.totalReadMsgs, 1)

	indata, err := sc.parser.Next()
	if err != nil {
		return nil, err
	}

	atomic.AddInt64(&sc.totalRead, int64(len(indata)))

	if bytes.HasPrefix(indata, clientHandshakeCompletedBytes) {
		return nil, mnet.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := sc.handleCINFO(); err != nil {
			sc.logs.WithTitle("websocketServerClient.write").With("client", sc.id).
				Error(err, "failed to pre-flush data")
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, clStatusBytes) {
		if err := sc.handleCLStatusReceive(indata); err != nil {
			history.WithTags("websocket-server").WithTitle("websocketServerClient.write").With("client", sc.id).
				Error(err, "failed to receive status request")
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	return indata, nil
}

func (sc *websocketServerClient) hasPending() bool {
	if err := sc.isAlive(); err != nil {
		return false
	}

	sc.bu.Lock()
	defer sc.bu.Unlock()
	if sc.wsWriter == nil {
		return false
	}

	return sc.wsWriter.Buffered() > 0
}

func (sc *websocketServerClient) buffered() (float64, error) {
	if err := sc.isAlive(); err != nil {
		return 0, err
	}

	sc.bu.Lock()
	defer sc.bu.Unlock()

	if sc.wsWriter == nil {
		return 0, mnet.ErrAlreadyClosed
	}

	available := float64(sc.wsWriter.Available())
	buffered := float64(sc.wsWriter.Buffered())
	return buffered / available, nil
}

func (sc *websocketServerClient) flush() error {
	if err := sc.isAlive(); err != nil {
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
		conn.SetWriteDeadline(time.Now().Add(mnet.MaxFlushDeadline))
		if err := sc.wsWriter.Flush(); err != nil {
			conn.SetWriteDeadline(time.Time{})
			sc.logs.WithTitle("websocketServerClient.flush").
				Error(err, "failed to receive status request")
			return err
		}
		conn.SetWriteDeadline(time.Time{})
	}

	return nil
}

func (sc *websocketServerClient) close() error {
	logs := sc.logs.WithTitle("websocketServerClient.close")
	if err := sc.isAlive(); err != nil {
		logs.Error(err, "closing websocket node connection")
		return err
	}

	logs.Info("closing websocket node connection")

	atomic.StoreInt64(&sc.closedCounter, 1)

	sc.flush()

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
		logs.Error(err, "closing websocket node connection")
	}

	return err
}

// readLoop handles the necessary operation of reading data from the
// underline connection.
func (sc *websocketServerClient) readLoop(conn net.Conn, reader *wsutil.Reader) {
	defer sc.close()
	defer sc.waiter.Done()

	lreader := internal.NewLengthRecvReader(reader, mnet.HeaderLength)
	incoming := make([]byte, mnet.SmallestMinBufferSize)

	logs := sc.logs.WithTitle("websocketServerClient.readLoop")

	for {

		wsFrame, err := reader.NextFrame()
		if err != nil {
			logs.With("remoteAddr", conn.RemoteAddr()).
				Error(err, "failed to read websocket data frame")
			return
		}

		frame, err := lreader.ReadHeader()
		if err != nil {
			logs.With("remoteAddr", conn.RemoteAddr()).
				Error(err, "failed to read data header")
			return
		}

		incoming = make([]byte, frame)

		n, err := lreader.Read(incoming)
		if err != nil {
			logs.With("remoteAddr", conn.RemoteAddr()).
				Error(err, "failed to read data header")
			return
		}

		datalogs := logs.WithFields(history.Attrs{
			"length":     n,
			"ws-frame":   wsFrame,
			"data":       string(incoming),
			"remoteAddr": conn.RemoteAddr(),
		})

		datalogs.Info("received websocket message")

		atomic.AddInt64(&sc.totalRead, int64(len(incoming[:n])))

		// Send into go-routine (critical path)?
		if err := sc.parser.Parse(incoming[:n]); err != nil {
			datalogs.Error(err, "failed to parse data")
			return
		}
	}
}

func (sc *websocketServerClient) getInfo() mnet.Info {
	var base mnet.Info
	if sc.isCluster() && sc.srcInfo != nil {
		base = *sc.srcInfo
	} else {
		base = mnet.Info{
			ID:         sc.id,
			ServerNode: true,
			Meta:       sc.network.Meta,
			Cluster:    sc.isACluster,
			MaxBuffer:  int64(sc.maxWrite),
			MinBuffer:  mnet.MinBufferSize,
			ServerAddr: sc.serverAddr.String(),
		}
	}

	if others, err := sc.network.getAllClient(sc.id); err != nil {
		for _, other := range others {
			if !other.IsCluster() {
				continue
			}
			base.ClusterNodes = append(base.ClusterNodes, other.Info())
		}
	}

	return base
}

func (sc *websocketServerClient) handleCLStatusReceive(data []byte) error {
	data = bytes.TrimPrefix(data, clStatusBytes)

	var info mnet.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	sc.srcInfo = &info
	sc.network.registerCluster(info.ClusterNodes)

	wc, err := sc.write(len(handshakeCompletedBytes))
	if err != nil {
		return err
	}

	wc.Write(handshakeCompletedBytes)
	wc.Close()
	return sc.flush()
}

func (sc *websocketServerClient) handleCLStatusSend() error {
	var info mnet.Info
	info.ID = sc.nid
	info.Cluster = true
	info.ServerNode = true
	info.Meta = sc.network.Meta
	info.MaxBuffer = int64(sc.maxWrite)
	info.MinBuffer = mnet.MinBufferSize
	info.ServerAddr = sc.serverAddr.String()

	if others, err := sc.network.getAllClient(sc.id); err != nil {
		for _, other := range others {
			if !other.IsCluster() {
				continue
			}
			info.ClusterNodes = append(info.ClusterNodes, other.Info())
		}
	}

	jsn, err := json.Marshal(info)
	if err != nil {
		return err
	}

	wc, err := sc.write(len(jsn) + len(clStatusBytes))
	if err != nil {
		return err
	}

	wc.Write(clStatusBytes)
	wc.Write(jsn)
	wc.Close()
	return sc.flush()
}

func (sc *websocketServerClient) handleCINFO() error {
	jsn, err := json.Marshal(sc.getInfo())
	if err != nil {
		return err
	}

	wc, err := sc.write(len(jsn) + len(rinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(rinfoBytes)
	wc.Write(jsn)
	wc.Close()
	return sc.flush()
}

func (sc *websocketServerClient) handleRINFO(data []byte) error {
	data = bytes.TrimPrefix(data, rinfoBytes)

	var info mnet.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	info.Cluster = sc.isACluster
	sc.srcInfo = &info

	sc.network.registerCluster(info.ClusterNodes)
	return nil
}

func (sc *websocketServerClient) sendCLHSCompleted() error {
	wc, err := sc.write(len(clientHandshakeCompletedBytes))
	if err != nil {
		return err
	}

	if _, err := wc.Write(clientHandshakeCompletedBytes); err != nil {
		return err
	}

	if err := wc.Close(); err != nil {
		return err
	}

	if err := sc.flush(); err != nil {
		return err
	}

	return nil
}

func (sc *websocketServerClient) sendCINFOReq() error {
	// Send to new client mnet.CINFO request
	wc, err := sc.write(len(cinfoBytes))
	if err != nil {
		return err
	}

	if _, err := wc.Write(cinfoBytes); err != nil {
		return err
	}

	if err := wc.Close(); err != nil {
		return err
	}

	if err := sc.flush(); err != nil {
		return err
	}

	return nil
}

func (sc *websocketServerClient) handshake() error {
	logs := sc.logs.WithTitle("websocketServerClient.handshake")

	if err := sc.sendCINFOReq(); err != nil {
		logs.Error(err, "error sending CINFO request")
		logs.Red("Failed to send CINFO request")
		return err
	}

	before := time.Now()

	// Wait for RCINFO response from connection.
	for {
		msg, err := sc.serverRead()
		if err != nil {
			if err != mnet.ErrNoDataYet {
				return err
			}

			if time.Now().Sub(before) > mnet.MaxInfoWait {
				sc.close()
				logs.Error(mnet.ErrFailedToRecieveInfo, "read timeout")
				logs.Red("read timeout")
				return mnet.ErrFailedToRecieveInfo
			}
			continue
		}

		// if we get a rescue signal, then client never got our CINFO request, so resent.
		if bytes.Equal(msg, rescueBytes) {
			logs.Info("received rescue request")

			if err := sc.sendCINFOReq(); err != nil {
				logs.Error(err, "failed to send CINFO after rescue request")
				logs.Red("send error with CINFO after rescue")
				return err
			}

			// reset time used for tracking timeout.
			before = time.Now()
			continue
		}

		// First message should be a mnet.RCINFO response.
		if !bytes.HasPrefix(msg, rinfoBytes) {
			sc.close()

			logs.Error(mnet.ErrFailedToRecieveInfo, "failed to receive RCINFO response")
			logs.Red("failed handshake at RCINFO response")
			return mnet.ErrFailedToRecieveInfo
		}

		if err := sc.handleRINFO(msg); err != nil {
			logs.Error(err, "failed to process RCINFO response")
			logs.Red("failed handshake at RCINFO response process")
			return err
		}

		break
	}

	// if its a cluster send Cluster Status message.
	if sc.isACluster {
		logs.Info("client cluster handshake agreement processing")

		if err := sc.handleCLStatusSend(); err != nil {
			logs.Error(err, "failed to send CLStatus message")
			logs.Red("failed handshake at CLStatus message")
			return err
		}

		before = time.Now()

		// Wait for handshake completion signal.
		for {
			msg, err := sc.serverRead()
			if err != nil {
				if err != mnet.ErrNoDataYet {
					return err
				}

				if time.Now().Sub(before) > mnet.MaxInfoWait {
					sc.close()
					logs.Error(mnet.ErrFailedToCompleteHandshake, "response timeout")
					logs.Red("failed to complete handshake")
					return mnet.ErrFailedToCompleteHandshake
				}
				continue
			}

			// if we get a rescue signal, then client never got our CLStatus response, so resend.
			if bytes.Equal(msg, rescueBytes) {
				if err := sc.handleCLStatusSend(); err != nil {
					logs.Error(err, "failed to send CLStatus response message")
					logs.Red("failed to complete handshake at CLStatus")
					return err
				}

				// reset time used for tracking timeout.
				before = time.Now()
				continue
			}

			if !bytes.Equal(msg, handshakeCompletedBytes) {
				logs.Error(mnet.ErrFailedToCompleteHandshake, "failed to received handshake completion")
				logs.Red("failed to complete handshake at completion signal")
				return mnet.ErrFailedToCompleteHandshake
			}

			break
		}
	} else {
		if err := sc.sendCLHSCompleted(); err != nil {
			logs.Error(err, "failed to deliver CLHS handshake completion signal")
			logs.Red("failed non-cluster handshake completion")
			return err
		}
	}

	logs.Info("handshake completed")

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

	totalClients int64
	totalClosed  int64
	totalActive  int64
	totalOpened  int64
	started      int64

	// Hook provides a means to get hook into the lifecycle-processes of
	// the network and client connection and disconnection.
	Hook mnet.Hook

	// Meta contains user defined gacts for this server which will be send along
	// the transfer. Always ensure not to keep large objects or info in here. It
	// is expected to be small.
	Meta mnet.Meta

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

	// MaxInfoWait defines the max time a connection awaits the completion of a
	// handshake phase.
	MaxInfoWait time.Duration

	// MaxWriteSize sets given max size of buffer for client, each client
	// writes collected till flush must not exceed else will not be buffered
	// and will be written directly.
	MaxWriteSize int

	logs   history.Ctx
	ctx    context.Context
	cancel func()

	raddr    net.Addr
	cu       sync.RWMutex
	clients  map[string]*websocketServerClient
	routines sync.WaitGroup
}

// Close ends the tcp listener and closes the underline network.
func (n *WebsocketNetwork) Close() error {
	if n.cancel != nil {
		n.cancel()
	}

	if n.ctx != nil {
		return n.ctx.Err()
	}

	return nil
}

// Addrs returns the net.Addr of the giving network.
func (n *WebsocketNetwork) Addrs() net.Addr {
	if n.raddr != nil {
		return n.raddr
	}

	addr, _ := net.ResolveUDPAddr("tcp", n.Addr)
	return addr
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

	n.ctx, n.cancel = context.WithCancel(ctx)

	if n.ID == "" {
		n.ID = uuid.NewV4().String()
	}

	if n.Upgrader == nil {
		n.Upgrader = &ws.Upgrader{}
	}

	if n.Dialer == nil {
		n.Dialer = &ws.Dialer{
			ReadBufferSize:  wsReadBuffer,
			WriteBufferSize: wsWriteBuffer,
			Timeout:         mnet.DefaultDialTimeout,
		}
	}

	n.Addr = netutils.GetAddr(n.Addr)
	if n.ServerName == "" {
		host, _, _ := net.SplitHostPort(n.Addr)
		n.ServerName = host
	}

	n.logs = history.WithTags("websocket-server").With("network-id", n.ID).
		With("serverName", n.ServerName).
		With("addr", n.Addr)

	defer n.logs.WithTitle("WebsocketNetwork.Start").Info("Started")

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

	if n.MaxWriteSize <= 0 {
		n.MaxWriteSize = mnet.MaxBufferSize
	}

	n.routines.Add(1)
	go n.handleConnections(n.ctx, stream)
	go func() {
		<-n.ctx.Done()
		stream.Close()
		if n.Hook != nil {
			n.Hook.NetworkClosed()
		}
	}()

	if n.Hook != nil {
		n.Hook.NetworkStarted()
	}

	return nil
}

func (n *WebsocketNetwork) registerCluster(clusters []mnet.Info) {
	for _, cluster := range clusters {
		if err := n.AddCluster(cluster.ServerAddr); err != nil {
			n.logs.WithTitle("WebsocketNetwork.registerClusters").
				With("info", cluster).
				Error(err, "failed to add cluster address")
		}
	}
}

// AddCluster attempts to add new connection to another mnet tcp server.
// It will attempt to dial specified address returning an error if the
// giving address failed or was not able to meet the handshake protocols.
func (n *WebsocketNetwork) AddCluster(addr string) error {
	if n.Addr == addr {
		return nil
	}

	log := n.logs.WithTitle("WebsocketNetwork.AddCluster").With("addr", addr)

	if n.connectedToMeByAddr(addr) {
		log.Error(mnet.ErrAlreadyServiced, "cluster already exists")
		log.Red("failed to cluster with network")
		return mnet.ErrAlreadyServiced
	}

	if !strings.HasPrefix(addr, "ws://") && !strings.HasPrefix(addr, "wss://") {
		if n.TLS == nil {
			addr = "ws://" + addr
		} else {
			addr = "wss://" + addr
		}
	}

	conn, _, hs, err := n.Dialer.Dial(context.Background(), addr)
	if err != nil {
		log.Error(err, "unable to dial network address")
		log.Red("failed to cluster with network")
		return err
	}

	if n.connectedToMeByAddr(conn.RemoteAddr().String()) {
		conn.Close()
		log.Error(mnet.ErrAlreadyServiced, "cluster address exists already")
		log.Red("failed to cluster with network")
		return mnet.ErrAlreadyServiced
	}

	policy := mnet.FunctionPolicy(n.MaxClusterRetries, func() error {
		return n.AddCluster(addr)
	}, mnet.ExponentialDelay(n.ClusterRetryDelay))

	if err := n.addWSClient(conn, hs, policy, true); err != nil {
		conn.Close()
		log.Error(mnet.ErrAlreadyServiced, "failed to add websocket cluster")
		log.Red("failed to cluster with network")
		return err
	}

	log.Info("cluster connection established")
	return nil
}

// connectedToMeByAddr returns true/false if we are already connected to server.
func (n *WebsocketNetwork) connectedToMeByAddr(addr string) bool {
	n.cu.Lock()
	defer n.cu.Unlock()

	for _, conn := range n.clients {
		if conn.srcInfo == nil {
			continue
		}

		if conn.srcInfo.ServerAddr == addr {
			return true
		}
	}

	return false
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
	log := n.logs.WithTitle("WebsocketNetwork.addClient").
		With("remote-addr", newConn.RemoteAddr()).
		With("local-addr", newConn.LocalAddr())

	handshake, err := n.Upgrader.Upgrade(newConn)
	if err != nil {
		log.Error(err, "Failed to upgrade net.Conn")
		return newConn.Close()
	}

	log.Info("net.Conn upgraded successfully")

	if err := n.addWSClient(newConn, handshake, policy, isCluster); err != nil {
		newConn.Close()
		log.Error(err, "net.Conn failed")
		return err
	}

	return nil
}

func (n *WebsocketNetwork) addWSClient(conn net.Conn, hs ws.Handshake, policy mnet.RetryPolicy, isCluster bool) error {
	log := n.logs.WithTitle("WebsocketNetwork.addWSClient").
		With("remote-addr", conn.RemoteAddr()).
		With("local-addr", conn.LocalAddr())

	atomic.AddInt64(&n.totalClients, 1)
	atomic.AddInt64(&n.totalOpened, 1)

	log.Info("Attempting to add net.Conn as websocket server client")

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
	client.network = n
	client.wshandshake = hs
	client.wsReader = wsReader
	client.wsWriter = wsWriter
	client.serverAddr = n.raddr
	client.isACluster = isCluster
	client.maxWrite = n.MaxWriteSize
	client.id = uuid.NewV4().String()
	client.localAddr = conn.LocalAddr()
	client.remoteAddr = conn.RemoteAddr()
	client.parser = new(internal.TaggedMessages)
	client.logs = n.logs.WithFields(map[string]interface{}{
		"server-addr": n.raddr,
		"client-id":   client.id,
		"server-id":   client.nid,
		"localAddr":   conn.LocalAddr(),
		"remoteAddr":  conn.RemoteAddr(),
	})

	client.waiter.Add(1)
	go client.readLoop(conn, wsReader)

	var mclient mnet.Client
	mclient.NID = n.ID
	mclient.ID = client.id
	mclient.CloseFunc = client.close
	mclient.WriteFunc = client.write
	mclient.FlushFunc = client.flush
	mclient.LiveFunc = client.isAlive
	mclient.LiveFunc = client.isAlive
	mclient.InfoFunc = client.getInfo
	mclient.BufferedFunc = client.buffered
	mclient.ReaderFunc = client.serverRead
	mclient.HasPendingFunc = client.hasPending
	mclient.LocalAddrFunc = client.getLocalAddr
	mclient.StatisticFunc = client.getStatistics
	mclient.RemoteAddrFunc = client.getRemoteAddr

	mclient.SiblingsFunc = func() ([]mnet.Client, error) {
		return n.getAllClient(client.id)
	}

	// Initial handshake protocol.
	if err := client.handshake(); err != nil {
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
			client.logs.Error(err, "Client connection handler failed")
			log.Red("Client handler stopped")
		}

		n.cu.Lock()
		delete(n.clients, client.id)
		n.cu.Unlock()

		if n.Hook != nil {
			if isCluster {
				n.Hook.ClusterDisconnected(mclient)
			} else {
				n.Hook.NodeDisconnected(mclient)
			}
		}

		if policy != nil {
			go func() {
				if err := n.isAlive(); err != nil {
					return
				}

				log.Info("Client calling retry policy")

				if err := policy.Retry(); err != nil {
					log.Error(err, "Client retry failed")
				}
			}()
		}
	}(mclient, client.remoteAddr)

	if n.Hook != nil {
		if isCluster {
			n.Hook.ClusterAdded(mclient)
		} else {
			n.Hook.NodeAdded(mclient)
		}
	}

	return nil
}

func (n *WebsocketNetwork) getAllClient(cid string) ([]mnet.Client, error) {
	n.cu.Lock()
	defer n.cu.Unlock()

	var clients []mnet.Client
	for _, conn := range n.clients {
		if conn.id == cid {
			continue
		}

		var client mnet.Client
		client.NID = n.ID
		client.ID = conn.id
		client.LiveFunc = conn.isAlive
		client.InfoFunc = conn.getInfo
		client.WriteFunc = conn.write
		client.FlushFunc = conn.flush
		client.BufferedFunc = conn.buffered
		client.HasPendingFunc = conn.hasPending
		client.StatisticFunc = conn.getStatistics
		client.RemoteAddrFunc = conn.getRemoteAddr
		client.LocalAddrFunc = conn.getLocalAddr
		client.SiblingsFunc = func() ([]mnet.Client, error) {
			return n.getAllClient(conn.id)
		}

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

	initial := mnet.MinTemporarySleep

	for {
		newConn, err := stream.ReadConn()
		if err != nil {
			n.logs.WithTitle("WebsocketNetwork.handleConnections").Error(err, "Failed to read connection")
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
		conn.close()
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
