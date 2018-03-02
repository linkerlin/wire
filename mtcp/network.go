package mtcp

import (
	"bufio"
	"crypto/tls"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"context"

	"io"

	"bytes"

	"encoding/json"

	"github.com/gokit/history"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/melon"
	"github.com/wirekit/wire"
	"github.com/wirekit/wire/internal"
	"github.com/wirekit/wire/mlisten"
	uuid "github.com/satori/go.uuid"
)

var (
	cinfoBytes                    = []byte(wire.CINFO)
	rinfoBytes                    = []byte(wire.RINFO)
	clStatusBytes                 = []byte(wire.CLSTATUS)
	rescueBytes                   = []byte(wire.CRESCUE)
	handshakeCompletedBytes       = []byte(wire.CLHANDSHAKECOMPLETED)
	clientHandshakeCompletedBytes = []byte(wire.ClientHandShakeCompleted)
)

//************************************************************************
//  TCP client on Server Implementation
//************************************************************************

type tcpServerClient struct {
	isACluster bool
	nid        string
	id         string
	serverAddr net.Addr
	localAddr  net.Addr
	remoteAddr net.Addr
	srcInfo    *wire.Info
	logs       history.Ctx
	network    *TCPNetwork
	worker     sync.WaitGroup
	do         sync.Once

	maxWrite int

	totalRead      int64
	totalWritten   int64
	totalFlushOut  int64
	totalWriteMsgs int64
	totalReadMsgs  int64
	closed         int64

	sos    *guardedBuffer
	parser *internal.TaggedMessages

	mu   sync.RWMutex
	conn net.Conn

	bu         sync.Mutex
	buffWriter *bufio.Writer
}

// isCluster returns true/false if a giving connection is actually
// connected to another server i.e a server-to-server and not a
// client-to-server connection.
func (nc *tcpServerClient) isCluster() bool {
	return nc.isACluster
}

// isLive returns an error if networkconn is disconnected from network.
func (nc *tcpServerClient) isLive() error {
	if atomic.LoadInt64(&nc.closed) == 1 {
		return wire.ErrAlreadyClosed
	}
	return nil
}

func (nc *tcpServerClient) getStatistics() (wire.ClientStatistic, error) {
	var stats wire.ClientStatistic
	stats.ID = nc.id
	stats.Local = nc.localAddr
	stats.Remote = nc.remoteAddr
	stats.BytesRead = atomic.LoadInt64(&nc.totalRead)
	stats.BytesFlushed = atomic.LoadInt64(&nc.totalFlushOut)
	stats.BytesWritten = atomic.LoadInt64(&nc.totalWritten)
	stats.MessagesRead = atomic.LoadInt64(&nc.totalReadMsgs)
	stats.MessagesWritten = atomic.LoadInt64(&nc.totalWriteMsgs)
	return stats, nil
}

func (nc *tcpServerClient) hasPending() bool {
	if err := nc.isLive(); err != nil {
		return false
	}

	nc.bu.Lock()
	defer nc.bu.Unlock()
	if nc.buffWriter == nil {
		return false
	}

	return nc.buffWriter.Buffered() > 0
}

func (nc *tcpServerClient) buffered() (float64, error) {
	if err := nc.isLive(); err != nil {
		return 0, err
	}

	nc.bu.Lock()
	defer nc.bu.Unlock()

	if nc.buffWriter == nil {
		return 0, wire.ErrAlreadyClosed
	}

	available := float64(nc.buffWriter.Available())
	buffered := float64(nc.buffWriter.Buffered())
	return buffered / available, nil
}

func (nc *tcpServerClient) flush() error {
	if err := nc.isLive(); err != nil {
		return err
	}

	var conn net.Conn
	nc.mu.RLock()
	conn = nc.conn
	nc.mu.RUnlock()

	if conn == nil {
		return wire.ErrAlreadyClosed
	}

	nc.bu.Lock()
	defer nc.bu.Unlock()
	if nc.buffWriter == nil {
		return wire.ErrAlreadyClosed
	}

	buffered := nc.buffWriter.Buffered()
	atomic.AddInt64(&nc.totalFlushOut, int64(buffered))

	conn.SetWriteDeadline(time.Now().Add(wire.MaxFlushDeadline))
	err := nc.buffWriter.Flush()
	conn.SetWriteDeadline(time.Time{})

	return err
}

// read reads from incoming message handling necessary
// handshake response and requests received over the wire,
// while returning non-hanshake messages.
func (nc *tcpServerClient) read() ([]byte, error) {
	if cerr := nc.isLive(); cerr != nil {
		return nil, cerr
	}

	atomic.AddInt64(&nc.totalReadMsgs, 1)

	indata, err := nc.parser.Next()
	if err != nil {
		return nil, err
	}

	atomic.AddInt64(&nc.totalRead, int64(len(indata)))

	if bytes.HasPrefix(indata, clientHandshakeCompletedBytes) {
		return nil, wire.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := nc.handleCINFO(); err != nil {
			return nil, err
		}
		return nil, wire.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, clStatusBytes) {
		if err := nc.handleCLStatusReceive(indata); err != nil {
			return nil, err
		}
		return nil, wire.ErrNoDataYet
	}

	return indata, nil
}

func (nc *tcpServerClient) getInfo() wire.Info {
	var base wire.Info
	if nc.isCluster() && nc.srcInfo != nil {
		base = *nc.srcInfo
	} else {
		base = wire.Info{
			ID:         nc.id,
			ServerNode: true,
			Cluster:    nc.isACluster,
			Meta:       nc.network.Meta,
			MaxBuffer:  int64(nc.maxWrite),
			MinBuffer:  wire.MinBufferSize,
			ServerAddr: nc.serverAddr.String(),
		}
	}

	if others, err := nc.network.getOtherClients(nc.id); err != nil {
		for _, other := range others {
			if !other.IsCluster() {
				continue
			}
			base.ClusterNodes = append(base.ClusterNodes, other.Info())
		}
	}

	return base
}

func (nc *tcpServerClient) handleCLStatusReceive(data []byte) error {
	data = bytes.TrimPrefix(data, clStatusBytes)

	var info wire.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	nc.srcInfo = &info
	nc.network.registerCluster(info.ClusterNodes)

	wc, err := nc.write(len(handshakeCompletedBytes))
	if err != nil {
		return err
	}

	wc.Write(handshakeCompletedBytes)
	wc.Close()
	return nc.flush()
}

func (nc *tcpServerClient) handleCLStatusSend() error {
	var info wire.Info
	info.Cluster = true
	info.ServerNode = true
	info.ID = nc.network.ID
	info.Meta = nc.network.Meta
	info.MaxBuffer = int64(nc.maxWrite)
	info.MinBuffer = wire.MinBufferSize
	info.ServerAddr = nc.network.raddr.String()

	if others, err := nc.network.getOtherClients(nc.id); err != nil {
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

	wc, err := nc.write(len(jsn) + len(clStatusBytes))
	if err != nil {
		return err
	}

	wc.Write(clStatusBytes)
	wc.Write(jsn)
	wc.Close()
	return nc.flush()
}

func (nc *tcpServerClient) handleCINFO() error {
	jsn, err := json.Marshal(nc.getInfo())
	if err != nil {
		return err
	}

	wc, err := nc.write(len(jsn) + len(rinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(rinfoBytes)
	wc.Write(jsn)
	wc.Close()
	return nc.flush()
}

func (nc *tcpServerClient) handleRINFO(data []byte) error {
	data = bytes.TrimPrefix(data, rinfoBytes)

	var info wire.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	info.Cluster = nc.isACluster
	nc.srcInfo = &info

	nc.network.registerCluster(info.ClusterNodes)
	return nil
}

func (nc *tcpServerClient) sendCLHSCompleted() error {
	wc, err := nc.write(len(clientHandshakeCompletedBytes))
	if err != nil {
		return err
	}

	if _, err := wc.Write(clientHandshakeCompletedBytes); err != nil {
		return err
	}

	if err := wc.Close(); err != nil {
		return err
	}

	if err := nc.flush(); err != nil {
		return err
	}

	return nil
}

func (nc *tcpServerClient) sendCINFOReq() error {
	wc, err := nc.write(len(cinfoBytes))
	if err != nil {
		return err
	}

	if _, err := wc.Write(cinfoBytes); err != nil {
		return err
	}

	if err := wc.Close(); err != nil {
		return err
	}

	if err := nc.flush(); err != nil {
		return err
	}

	return nil
}

func (nc *tcpServerClient) handshake() error {
	if err := nc.sendCINFOReq(); err != nil {
		nc.logs.Error(err, "error sending CINFO request")
		nc.logs.Red("Failed to send CINFO request")
		return err
	}

	before := time.Now()

	// Wait for RCINFO response from connection.
	for {
		msg, err := nc.read()
		if err != nil {
			if err != wire.ErrNoDataYet {
				return err
			}

			if time.Now().Sub(before) > wire.MaxInfoWait {
				nc.closeConn()
				nc.logs.Error(wire.ErrFailedToRecieveInfo, "read timeout")
				nc.logs.Red("read timeout")
				return wire.ErrFailedToRecieveInfo
			}
			continue
		}

		// if we get a rescue signal, then client never got our CINFO request, so resent.
		if bytes.Equal(msg, rescueBytes) {
			nc.logs.Info("received rescue request")
			if err := nc.sendCINFOReq(); err != nil {
				nc.logs.Error(err, "failed to send CINFO after rescue request")
				nc.logs.Red("send error with CINFO after rescue")
				return err
			}

			// reset time used for tracking timeout.
			before = time.Now()
			continue
		}

		// First message should be a wire.RCINFO response.
		if !bytes.HasPrefix(msg, rinfoBytes) {
			nc.closeConn()
			nc.logs.Error(wire.ErrFailedToRecieveInfo, "failed to receive RCINFO response")
			nc.logs.Red("failed handshake at RCINFO response")
			return wire.ErrFailedToRecieveInfo
		}

		if err := nc.handleRINFO(msg); err != nil {
			nc.logs.Error(err, "failed to process RCINFO response")
			nc.logs.Red("failed handshake at RCINFO response process")
			return err
		}

		break
	}

	// if its a cluster send Cluster Status message.
	if nc.isACluster {
		nc.logs.Info("client cluster handshake agreement processing")
		if err := nc.handleCLStatusSend(); err != nil {
			nc.logs.Error(err, "failed to send CLStatus message")
			nc.logs.Red("failed handshake at CLStatus message")
			return err
		}

		before = time.Now()

		// Wait for handshake completion signal.
		for {
			msg, err := nc.read()
			if err != nil {
				if err != wire.ErrNoDataYet {
					return err
				}

				if time.Now().Sub(before) > wire.MaxInfoWait {
					nc.closeConn()
					nc.logs.Error(wire.ErrFailedToCompleteHandshake, "response timeout")
					nc.logs.Red("failed to complete handshake")
					return wire.ErrFailedToCompleteHandshake
				}
				continue
			}

			// if we get a rescue signal, then client never got our CLStatus response, so resend.
			if bytes.Equal(msg, rescueBytes) {
				if err := nc.handleCLStatusSend(); err != nil {
					nc.logs.Error(err, "failed to send CLStatus response message")
					nc.logs.Red("failed to complete handshake at CLStatus")
					return err
				}

				// reset time used for tracking timeout.
				before = time.Now()
				continue
			}

			if !bytes.Equal(msg, handshakeCompletedBytes) {
				nc.logs.Error(wire.ErrFailedToCompleteHandshake, "failed to received handshake completion")
				nc.logs.Red("failed to complete handshake at completion signal")
				return wire.ErrFailedToCompleteHandshake
			}

			break
		}
	} else {
		if err := nc.sendCLHSCompleted(); err != nil {
			nc.logs.Error(err, "failed to deliver CLHS handshake completion signal")
			nc.logs.Red("failed non-cluster handshake completion")
			return err
		}
	}

	nc.logs.Info("handshake completed")

	return nil
}

func (nc *tcpServerClient) write(inSize int) (io.WriteCloser, error) {
	if err := nc.isLive(); err != nil {
		return nil, err
	}

	var conn net.Conn
	nc.mu.RLock()
	conn = nc.conn
	nc.mu.RUnlock()

	if conn == nil {
		return nil, wire.ErrAlreadyClosed
	}

	return internal.NewActionLengthWriter(func(size []byte, data []byte) error {
		atomic.AddInt64(&nc.totalWritten, 1)
		atomic.AddInt64(&nc.totalWritten, int64(len(data)))

		nc.bu.Lock()
		defer nc.bu.Unlock()

		if nc.buffWriter == nil {
			return wire.ErrAlreadyClosed
		}

		//available := nc.buffWriter.Available()
		buffered := nc.buffWriter.Buffered()
		atomic.AddInt64(&nc.totalFlushOut, int64(buffered))

		// size of next write.
		toWrite := buffered + len(data)

		// add size header
		toWrite += wire.HeaderLength

		if toWrite >= nc.maxWrite {
			conn.SetWriteDeadline(time.Now().Add(wire.MaxFlushDeadline))
			if err := nc.buffWriter.Flush(); err != nil {
				conn.SetWriteDeadline(time.Time{})
				return err
			}
			conn.SetWriteDeadline(time.Time{})
		}

		if _, err := nc.buffWriter.Write(size); err != nil {
			return err
		}

		if _, err := nc.buffWriter.Write(data); err != nil {
			return err
		}

		return nil
	}, wire.HeaderLength, inSize), nil
}

func (nc *tcpServerClient) closeConnection() error {
	if err := nc.isLive(); err != nil {
		nc.logs.Error(err, "client already closed")
		return err
	}

	atomic.StoreInt64(&nc.closed, 1)

	nc.flush()

	nc.mu.Lock()
	if nc.conn == nil {
		nc.logs.Yellow("client already closed")
		return wire.ErrAlreadyClosed
	}
	err := nc.conn.Close()
	nc.mu.Unlock()

	nc.worker.Wait()

	nc.mu.Lock()
	nc.conn = nil
	nc.mu.Unlock()

	nc.bu.Lock()
	nc.buffWriter = nil
	nc.bu.Unlock()

	nc.logs.Info("client closed")
	return err
}

func (nc *tcpServerClient) getRemoteAddr() (net.Addr, error) {
	return nc.remoteAddr, nil
}

func (nc *tcpServerClient) getLocalAddr() (net.Addr, error) {
	return nc.localAddr, nil
}

func (nc *tcpServerClient) closeConn() error {
	return nc.closeConnection()
}

// readLoop handles the necessary operation of reading data from the
// underline connection.
func (nc *tcpServerClient) readLoop() {
	defer nc.closeConnection()
	defer nc.worker.Done()

	var cn net.Conn
	nc.mu.RLock()
	if nc.conn == nil {
		nc.mu.RUnlock()
		return
	}
	cn = nc.conn
	nc.mu.RUnlock()

	connReader := bufio.NewReaderSize(cn, nc.maxWrite)
	lreader := internal.NewLengthRecvReader(connReader, wire.HeaderLength)

	var incoming []byte

	for {
		frame, err := lreader.ReadHeader()
		if err != nil {
			nc.logs.Error(err, "read header error")
			return
		}

		incoming = make([]byte, frame)
		n, err := lreader.Read(incoming)
		if err != nil {
			nc.logs.Error(err, "read error")
			return
		}

		atomic.AddInt64(&nc.totalRead, int64(len(incoming)))

		datalog := nc.logs.With("data", string(incoming)).Info("received data")

		// Send into go-routine (critical path)?
		if err := nc.parser.Parse(incoming[:n]); err != nil {
			datalog.Error(err, "parser data error")
			return
		}
	}
}

//************************************************************************
//  TCP Server Implementation
//************************************************************************

// TCPNetwork defines a network which runs ontop of provided wire.ConnHandler.
type TCPNetwork struct {
	ID         string
	Addr       string
	ServerName string
	TLS        *tls.Config
	Handler    wire.ConnHandler

	totalClients int64
	totalClosed  int64
	totalActive  int64
	totalOpened  int64

	// Hook provides a means to get hook into the lifecycle-processes of
	// the network and client connection and disconnection.
	Hook wire.Hook

	// Dialer to be used to create net.Conn to connect to cluster address
	// in TCPNetwork.AddCluster. Set to use a custom dialer.
	Dialer *net.Dialer

	// Meta contains user defined gacts for this server which will be send along
	// the transfer. Always ensure not to keep large objects or info in here. It
	// is expected to be small.
	Meta wire.Meta

	// MaxConnection defines the total allowed connections for
	// giving network.
	MaxConnections int

	// MaxClusterRetries specifies allowed max retries used by the
	// RetryPolicy for reconnecting to a disconnected cluster network.
	MaxClusterRetries int

	// ClusterRetryDelay defines the duration used to expand for
	// each retry of reconnecting to a cluster address.
	ClusterRetryDelay time.Duration

	// MaxWriteSize sets given max size of buffer for client, each client writes collected
	// till flush must not exceed else will not be buffered and will be written directly.
	MaxWriteSize int

	logs   history.Ctx
	ctx    context.Context
	cancel func()

	raddr    net.Addr
	pool     chan func()
	cu       sync.RWMutex
	clients  map[string]*tcpServerClient
	routines sync.WaitGroup
}

// Close ends the tcp listener and closes the underline network.
func (n *TCPNetwork) Close() error {
	if n.cancel != nil {
		n.cancel()
	}

	if n.ctx != nil {
		return n.ctx.Err()
	}

	return nil
}

// Start initializes the network listener.
func (n *TCPNetwork) Start(ctx context.Context) error {
	n.ctx, n.cancel = context.WithCancel(ctx)

	if n.ID == "" {
		n.ID = uuid.NewV4().String()
	}

	n.Addr = netutils.GetAddr(n.Addr)
	if n.ServerName == "" {
		host, _, _ := net.SplitHostPort(n.Addr)
		n.ServerName = host
	}

	if n.Dialer == nil {
		n.Dialer = &net.Dialer{Timeout: wire.DefaultDialTimeout}
	}

	if n.TLS != nil && !n.TLS.InsecureSkipVerify {
		n.TLS.ServerName = n.ServerName
	}

	stream, err := mlisten.Listen("tcp", n.Addr, n.TLS)
	if err != nil {
		return err
	}

	n.raddr = stream.Addr()
	n.pool = make(chan func(), 0)
	n.clients = make(map[string]*tcpServerClient)

	n.logs = history.WithFields(map[string]interface{}{
		"addr":        n.raddr,
		"network-id":  n.ID,
		"server-name": n.ServerName,
	})

	if n.MaxWriteSize <= 0 {
		n.MaxWriteSize = wire.MaxBufferSize
	}

	if n.MaxConnections <= 0 {
		n.MaxConnections = wire.MaxConnections
	}

	if n.MaxClusterRetries <= 0 {
		n.MaxClusterRetries = wire.MaxReconnectRetries
	}

	if n.ClusterRetryDelay <= 0 {
		n.ClusterRetryDelay = wire.DefaultClusterRetryDelay
	}

	n.routines.Add(2)
	go n.runStream(stream)
	go n.handleClose(n.ctx, stream)

	if n.Hook != nil {
		n.Hook.NetworkStarted()
	}

	return nil
}

func (n *TCPNetwork) registerCluster(clusters []wire.Info) {
	for _, cluster := range clusters {
		if err := n.AddCluster(cluster.ServerAddr); err != nil {
			n.logs.With("info", cluster).Error(err, "failed to add cluster address")
		}
	}
}

func (n *TCPNetwork) handleClose(ctx context.Context, stream melon.ConnReadWriteCloser) {
	defer n.routines.Done()

	<-ctx.Done()

	n.cu.RLock()
	for _, conn := range n.clients {
		n.cu.RUnlock()
		conn.closeConnection()
		n.cu.RLock()
	}
	n.cu.RUnlock()

	if err := stream.Close(); err != nil {
		n.logs.Error(err, "closing listener")
		n.logs.Red("network listener closed")
	}

	if n.Hook != nil {
		n.Hook.NetworkClosed()
	}

	n.logs.Info("network closed")
}

// Statistics returns statics associated with TCPNetwork.
func (n *TCPNetwork) Statistics() wire.NetworkStatistic {
	var stats wire.NetworkStatistic
	stats.ID = n.ID
	stats.LocalAddr = n.raddr
	stats.RemoteAddr = n.raddr
	stats.TotalClients = atomic.LoadInt64(&n.totalClients)
	stats.TotalClosed = atomic.LoadInt64(&n.totalClosed)
	stats.TotalOpened = atomic.LoadInt64(&n.totalOpened)
	return stats
}

// Wait is called to ensure network ended.
func (n *TCPNetwork) Wait() {
	n.routines.Wait()
}

func (n *TCPNetwork) getOtherClients(cid string) ([]wire.Client, error) {
	n.cu.Lock()
	defer n.cu.Unlock()

	var clients []wire.Client
	for id, conn := range n.clients {
		if id == cid {
			continue
		}

		var client wire.Client
		client.NID = n.ID
		client.ID = conn.id
		client.FlushFunc = conn.flush
		client.LiveFunc = conn.isLive
		client.WriteFunc = conn.write
		client.InfoFunc = conn.getInfo
		client.BufferedFunc = conn.buffered
		client.IsClusterFunc = conn.isCluster
		client.HasPendingFunc = conn.hasPending
		client.StatisticFunc = conn.getStatistics
		client.RemoteAddrFunc = conn.getRemoteAddr
		client.LocalAddrFunc = conn.getLocalAddr
		client.SiblingsFunc = func() ([]wire.Client, error) {
			return n.getOtherClients(cid)
		}

		clients = append(clients, client)
	}

	return clients, nil
}

// AddCluster attempts to add new connection to another mnet tcp server.
// It will attempt to dial specified address returning an error if the
// giving address failed or was not able to meet the handshake protocols.
func (n *TCPNetwork) AddCluster(addr string) error {
	if n.Addr == addr {
		return nil
	}

	if n.connectedToMeByAddr(addr) {
		return wire.ErrAlreadyServiced
	}

	var err error
	var conn net.Conn

	if n.TLS == nil {
		conn, err = n.Dialer.Dial("tcp", addr)
	} else {
		conn, err = tls.DialWithDialer(n.Dialer, "tcp", addr, n.TLS)
	}

	if err != nil {
		return err
	}

	if n.connectedToMeByAddr(conn.RemoteAddr().String()) {
		conn.Close()
		return wire.ErrAlreadyServiced
	}

	policy := wire.FunctionPolicy(n.MaxClusterRetries, func() error {
		return n.AddCluster(addr)
	}, wire.ExponentialDelay(n.ClusterRetryDelay))

	if err := n.addClient(conn, policy, true); err != nil {
		conn.Close()
		return err
	}

	return nil
}

// connectedToMeByAddr returns true/false if we are already connected to server.
func (n *TCPNetwork) connectedToMeByAddr(addr string) bool {
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
func (n *TCPNetwork) connectedToMe(conn net.Conn) bool {
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

func (n *TCPNetwork) addClient(conn net.Conn, policy wire.RetryPolicy, isCluster bool) error {
	defer atomic.AddInt64(&n.totalOpened, -1)
	defer atomic.AddInt64(&n.totalClosed, 1)

	id := uuid.NewV4().String()

	client := wire.Client{
		ID:  id,
		NID: n.ID,
	}

	nc := new(tcpServerClient)
	nc.id = id
	nc.nid = n.ID
	nc.conn = conn
	nc.network = n
	nc.serverAddr = n.raddr
	nc.isACluster = isCluster
	nc.localAddr = conn.LocalAddr()
	nc.remoteAddr = conn.RemoteAddr()
	nc.maxWrite = n.MaxWriteSize
	nc.sos = newGuardedBuffer(512)
	nc.parser = new(internal.TaggedMessages)
	nc.buffWriter = bufio.NewWriterSize(conn, n.MaxWriteSize)

	nc.logs = n.logs.WithFields(map[string]interface{}{
		"server-addr": n.raddr,
		"client-id":   nc.id,
		"server-id":   nc.nid,
		"localAddr":   conn.LocalAddr(),
		"remoteAddr":  conn.RemoteAddr(),
	})

	client.LiveFunc = nc.isLive
	client.InfoFunc = nc.getInfo
	client.ReaderFunc = nc.read
	client.WriteFunc = nc.write
	client.FlushFunc = nc.flush
	client.CloseFunc = nc.closeConn
	client.BufferedFunc = nc.buffered
	client.IsClusterFunc = nc.isCluster
	client.HasPendingFunc = nc.hasPending
	client.LocalAddrFunc = nc.getLocalAddr
	client.StatisticFunc = nc.getStatistics
	client.RemoteAddrFunc = nc.getRemoteAddr
	client.SiblingsFunc = func() ([]wire.Client, error) {
		return n.getOtherClients(nc.id)
	}

	nc.worker.Add(1)
	go nc.readLoop()

	if err := nc.handshake(); err != nil {
		return err
	}

	n.cu.Lock()
	n.clients[id] = nc
	n.cu.Unlock()

	atomic.AddInt64(&n.totalClients, 1)
	go func() {
		if err := n.Handler(client); err != nil {
			nc.logs.Error(err, "Client connection handler failed")
			nc.closeConnection()
		}

		n.cu.Lock()
		delete(n.clients, id)
		n.cu.Unlock()

		if n.Hook != nil {
			if isCluster {
				n.Hook.ClusterDisconnected(client)
			} else {
				n.Hook.NodeDisconnected(client)
			}
		}

		if policy != nil {
			go func() {
				if err := policy.Retry(); err != nil {
					n.logs.Error(err, "Client retry failed")
				}
			}()
		}
	}()

	if n.Hook != nil {
		if isCluster {
			n.Hook.ClusterAdded(client)
		} else {
			n.Hook.NodeAdded(client)
		}
	}

	return nil
}

// Addrs returns the net.Addr of the giving network.
func (n *TCPNetwork) Addrs() net.Addr {
	if n.raddr != nil {
		return n.raddr
	}

	addr, _ := net.ResolveUDPAddr("tcp", n.Addr)
	return addr
}

// runStream runs the process of listening for new connections and
// creating appropriate client objects which will handle behaviours
// appropriately.
func (n *TCPNetwork) runStream(stream melon.ConnReadWriteCloser) {
	defer n.routines.Done()

	initial := wire.MinTemporarySleep

	for {
		newConn, err := stream.ReadConn()
		if err != nil {
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

				if initial >= wire.MaxTemporarySleep {
					initial = wire.MinTemporarySleep
				}
			}

			continue
		}

		// if we have maximum allowed connections, then close new conn.
		if int(atomic.LoadInt64(&n.totalOpened)) >= n.MaxConnections {
			newConn.Close()
			continue
		}

		atomic.AddInt64(&n.totalClients, 1)
		atomic.AddInt64(&n.totalOpened, 1)
		n.addClient(newConn, nil, false)
	}
}

type guardedBuffer struct {
	ml sync.Mutex
	bu *bytes.Buffer
}

func newGuardedBuffer(size int) *guardedBuffer {
	return &guardedBuffer{
		bu: bytes.NewBuffer(make([]byte, 0, size)),
	}
}

func (gb *guardedBuffer) Do(b func(*bytes.Buffer)) {
	gb.ml.Lock()
	defer gb.ml.Unlock()
	b(gb.bu)
}
