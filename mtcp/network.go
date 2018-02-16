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

	"github.com/influx6/faux/metrics"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/faux/pools/done"
	"github.com/influx6/melon"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/internal"
	"github.com/influx6/mnet/mlisten"
	uuid "github.com/satori/go.uuid"
)

var (
	cinfoBytes              = []byte(mnet.CINFO)
	rinfoBytes              = []byte(mnet.RINFO)
	clStatusBytes           = []byte(mnet.CLSTATUS)
	handshakeCompletedBytes = []byte(mnet.CLHANDSHAKECOMPLETED)
	bufferPool              = done.NewDonePool(218, 20)
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
	srcInfo    *mnet.Info
	network    *TCPNetwork
	metrics    metrics.Metrics
	worker     sync.WaitGroup
	do         sync.Once

	maxWrite    int
	maxDeadline time.Duration
	maxInfoWait time.Duration

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
func (nc *tcpServerClient) isCluster(cm mnet.Client) bool {
	return nc.isACluster
}

// isLive returns an error if networkconn is disconnected from network.
func (nc *tcpServerClient) isLive(cm mnet.Client) error {
	if atomic.LoadInt64(&nc.closed) == 1 {
		return mnet.ErrAlreadyClosed
	}
	return nil
}

func (nc *tcpServerClient) getStatistics(cm mnet.Client) (mnet.ClientStatistic, error) {
	var stats mnet.ClientStatistic
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

func (nc *tcpServerClient) flush(cm mnet.Client) error {
	if err := nc.isLive(cm); err != nil {
		return err
	}

	var conn net.Conn
	nc.mu.RLock()
	conn = nc.conn
	nc.mu.RUnlock()

	if conn == nil {
		return mnet.ErrAlreadyClosed
	}

	nc.bu.Lock()
	defer nc.bu.Unlock()
	if nc.buffWriter == nil {
		return mnet.ErrAlreadyClosed
	}

	buffered := nc.buffWriter.Buffered()
	atomic.AddInt64(&nc.totalFlushOut, int64(buffered))

	conn.SetWriteDeadline(time.Now().Add(nc.maxDeadline))
	err := nc.buffWriter.Flush()
	conn.SetWriteDeadline(time.Time{})

	return err
}

// read reads from incoming message handling necessary
// handshake response and requests received over the wire,
// while returning non-hanshake messages.
func (nc *tcpServerClient) read(cm mnet.Client) ([]byte, error) {
	if cerr := nc.isLive(cm); cerr != nil {
		return nil, cerr
	}

	atomic.AddInt64(&nc.totalReadMsgs, 1)

	indata, err := nc.parser.Next()
	if err != nil {
		return nil, err
	}

	atomic.AddInt64(&nc.totalRead, int64(len(indata)))

	if bytes.HasPrefix(indata, cinfoBytes) {
		if err := nc.handleCINFO(cm); err != nil {
			nc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(nc.id),
				metrics.With("client", nc.id),
				metrics.With("network", nc.nid),
				metrics.Message("handleCINFO failed"),
			)
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	if bytes.HasPrefix(indata, clStatusBytes) {
		if err := nc.handleCLStatusReceive(cm, indata); err != nil {
			nc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(nc.id),
				metrics.With("client", nc.id),
				metrics.With("network", nc.nid),
				metrics.Message("handleCLStatusRecieve failed"),
			)
			return nil, err
		}
		return nil, mnet.ErrNoDataYet
	}

	return indata, nil
}

func (nc *tcpServerClient) getInfo(cm mnet.Client) mnet.Info {
	var base mnet.Info
	if nc.isCluster(cm) && nc.srcInfo != nil {
		base = *nc.srcInfo
	} else {
		base = mnet.Info{
			ID:         nc.id,
			ServerNode: true,
			Cluster:    nc.isACluster,
			MaxBuffer:  int64(nc.maxWrite),
			MinBuffer:  mnet.MinBufferSize,
			ServerAddr: nc.serverAddr.String(),
		}
	}

	return base
}

func (nc *tcpServerClient) handleCLStatusReceive(cm mnet.Client, data []byte) error {
	data = bytes.TrimPrefix(data, clStatusBytes)

	var info mnet.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	nc.srcInfo = &info

	wc, err := nc.write(cm, len(handshakeCompletedBytes))
	if err != nil {
		return err
	}

	wc.Write(handshakeCompletedBytes)
	wc.Close()
	return nc.flush(cm)
}

func (nc *tcpServerClient) handleCLStatusSend(cm mnet.Client) error {
	var info mnet.Info
	info.Cluster = true
	info.ServerNode = true
	info.ID = nc.network.ID
	info.MaxBuffer = int64(nc.maxWrite)
	info.MinBuffer = mnet.MinBufferSize
	info.ServerAddr = nc.network.raddr.String()

	jsn, err := json.Marshal(info)
	if err != nil {
		return err
	}

	wc, err := nc.write(cm, len(jsn)+len(clStatusBytes))
	if err != nil {
		return err
	}

	wc.Write(clStatusBytes)
	wc.Write(jsn)
	wc.Close()
	return nc.flush(cm)
}

func (nc *tcpServerClient) handleCINFO(cm mnet.Client) error {
	jsn, err := json.Marshal(nc.getInfo(cm))
	if err != nil {
		return err
	}

	wc, err := nc.write(cm, len(jsn)+len(rinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(rinfoBytes)
	wc.Write(jsn)
	wc.Close()
	return nc.flush(cm)
}

func (nc *tcpServerClient) handleRINFO(data []byte) error {
	data = bytes.TrimPrefix(data, rinfoBytes)

	var info mnet.Info
	if err := json.Unmarshal(data, &info); err != nil {
		return err
	}

	info.Cluster = nc.isACluster
	nc.srcInfo = &info
	return nil
}

func (nc *tcpServerClient) handshake(cm mnet.Client) error {
	// Send to new client mnet.CINFO request
	wc, err := nc.write(cm, len(cinfoBytes))
	if err != nil {
		return err
	}

	wc.Write(cinfoBytes)
	wc.Close()
	nc.flush(cm)

	before := time.Now()

	// Wait for RCINFO response from connection.
	for {
		msg, err := nc.read(cm)
		if err != nil {
			if err != mnet.ErrNoDataYet {
				return err
			}

			//time.Sleep(mnet.MaxInfoWaitSleep)
			continue
		}

		if time.Now().Sub(before) > nc.maxInfoWait {
			nc.closeConn(cm)
			nc.metrics.Emit(
				metrics.Error(mnet.ErrFailedToRecieveInfo),
				metrics.WithID(nc.id),
				metrics.With("client", nc.id),
				metrics.With("network", nc.nid),
				metrics.Message("Timeout: awaiting mnet.RCINFo data"),
			)
			return mnet.ErrFailedToRecieveInfo
		}

		// First message should be a mnet.RCINFO response.
		if !bytes.HasPrefix(msg, rinfoBytes) {
			nc.closeConn(cm)
			nc.metrics.Emit(
				metrics.Error(mnet.ErrFailedToRecieveInfo),
				metrics.WithID(nc.id),
				metrics.With("client", nc.id),
				metrics.With("network", nc.nid),
				metrics.With("data", string(msg)),
				metrics.Message("Invalid mnet.RCINFO prefix"),
			)
			return mnet.ErrFailedToRecieveInfo
		}

		if err := nc.handleRINFO(msg); err != nil {
			nc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(nc.id),
				metrics.With("client", nc.id),
				metrics.With("network", nc.nid),
				metrics.With("data", string(msg)),
				metrics.Message("Invalid mnet.RCINFO data"),
			)
			return err
		}

		break
	}

	// if its a cluster send Cluster Status message.
	if nc.isACluster {
		if err := nc.handleCLStatusSend(cm); err != nil {
			return err
		}

		before = time.Now()

		// Wait for handshake completion signal.
		for {
			msg, err := nc.read(cm)
			if err != nil {
				if err != mnet.ErrNoDataYet {
					return err
				}

				//time.Sleep(mnet.MaxInfoWaitSleep)
				continue
			}

			if time.Now().Sub(before) > nc.maxInfoWait {
				nc.closeConn(cm)
				nc.metrics.Emit(
					metrics.Error(mnet.ErrFailedToCompleteHandshake),
					metrics.WithID(nc.id),
					metrics.With("client", nc.id),
					metrics.With("network", nc.nid),
					metrics.Message("Failed to receive handshake completion before max wait"),
				)
				return mnet.ErrFailedToCompleteHandshake
			}

			if !bytes.Equal(msg, handshakeCompletedBytes) {
				nc.metrics.Emit(
					metrics.Error(mnet.ErrFailedToCompleteHandshake),
					metrics.WithID(nc.id),
					metrics.With("client", nc.id),
					metrics.With("network", nc.nid),
					metrics.With("data", string(msg)),
					metrics.Message("Invalid handshake completion data"),
				)
				return mnet.ErrFailedToCompleteHandshake
			}

			break
		}
	}

	return nil
}

func (nc *tcpServerClient) write(cm mnet.Client, inSize int) (io.WriteCloser, error) {
	if err := nc.isLive(cm); err != nil {
		return nil, err
	}

	var conn net.Conn
	nc.mu.RLock()
	conn = nc.conn
	nc.mu.RUnlock()

	if conn == nil {
		return nil, mnet.ErrAlreadyClosed
	}

	//return bufferPool.Get(inSize, func(incoming int, w io.WriterTo) error {
	//	atomic.AddInt64(&nc.totalWriteMsgs, 1)
	//	atomic.AddInt64(&nc.totalWritten, int64(incoming))
	//
	//	nc.bu.Lock()
	//	defer nc.bu.Unlock()
	//
	//	if nc.buffWriter == nil {
	//		return mnet.ErrAlreadyClosed
	//	}
	//
	//	//available := nc.buffWriter.Available()
	//	buffered := nc.buffWriter.Buffered()
	//	atomic.AddInt64(&nc.totalFlushOut, int64(buffered))
	//
	//	// size of next write.
	//	toWrite := buffered + incoming
	//
	//	// add size header
	//	toWrite += mnet.HeaderLength
	//
	//	if toWrite >= nc.maxWrite {
	//		conn.SetWriteDeadline(time.Now().Add(nc.maxDeadline))
	//		if err := nc.buffWriter.Flush(); err != nil {
	//			conn.SetWriteDeadline(time.Time{})
	//			return err
	//		}
	//		conn.SetWriteDeadline(time.Time{})
	//	}
	//
	//	// write length header first.
	//	header := make([]byte, mnet.HeaderLength)
	//	binary.BigEndian.PutUint32(header, uint32(incoming))
	//	nc.buffWriter.Write(header)
	//
	//	// then flush data alongside header.
	//	_, err := w.WriteTo(nc.buffWriter)
	//	return err
	//}), nil

	return internal.NewActionLengthWriter(func(size []byte, data []byte) error {
		atomic.AddInt64(&nc.totalWritten, 1)
		atomic.AddInt64(&nc.totalWritten, int64(len(data)))

		nc.bu.Lock()
		defer nc.bu.Unlock()

		if nc.buffWriter == nil {
			return mnet.ErrAlreadyClosed
		}

		//available := nc.buffWriter.Available()
		buffered := nc.buffWriter.Buffered()
		atomic.AddInt64(&nc.totalFlushOut, int64(buffered))

		// size of next write.
		toWrite := buffered + len(data)

		// add size header
		toWrite += mnet.HeaderLength

		if toWrite >= nc.maxWrite {
			conn.SetWriteDeadline(time.Now().Add(nc.maxDeadline))
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
	}, mnet.HeaderLength, inSize), nil
}

func (nc *tcpServerClient) closeConnection(cm mnet.Client) error {
	if err := nc.isLive(cm); err != nil {
		return err
	}

	defer nc.metrics.Emit(
		metrics.WithID(nc.id),
		metrics.With("network", nc.nid),
		metrics.Message("tcpServerClient.closeConnection"),
	)

	atomic.StoreInt64(&nc.closed, 1)

	nc.flush(cm)

	nc.mu.Lock()
	if nc.conn == nil {
		return mnet.ErrAlreadyClosed
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

	return err
}

func (nc *tcpServerClient) getRemoteAddr(cm mnet.Client) (net.Addr, error) {
	return nc.remoteAddr, nil
}

func (nc *tcpServerClient) getLocalAddr(cm mnet.Client) (net.Addr, error) {
	return nc.localAddr, nil
}

func (nc *tcpServerClient) closeConn(cm mnet.Client) error {
	return nc.closeConnection(cm)
}

// readLoop handles the necessary operation of reading data from the
// underline connection.
func (nc *tcpServerClient) readLoop(cm mnet.Client) {
	defer nc.closeConnection(cm)
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
	lreader := internal.NewLengthReader(connReader, mnet.HeaderLength)

	for {
		incoming, err := lreader.Read()
		if err != nil {
			nc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(nc.id),
				metrics.With("length", len(incoming)),
				metrics.With("data", string(incoming)),
				metrics.Message("Connection failed to read: closing"),
				metrics.With("network", nc.nid),
			)
			return
		}

		atomic.AddInt64(&nc.totalRead, int64(len(incoming)))

		// Send into go-routine (critical path)?
		if err := nc.parser.Parse(incoming); err != nil {
			nc.metrics.Emit(
				metrics.Error(err),
				metrics.WithID(nc.id),
				metrics.Message("ParseError"),
				metrics.With("network", nc.nid),
				metrics.With("length", len(incoming)),
				metrics.With("data", string(incoming)),
			)
			return
		}
	}
}

//************************************************************************
//  TCP Server Implementation
//************************************************************************

// TCPNetwork defines a network which runs ontop of provided mnet.ConnHandler.
type TCPNetwork struct {
	ID         string
	Addr       string
	ServerName string
	TLS        *tls.Config
	Handler    mnet.ConnHandler
	Metrics    metrics.Metrics

	// Dialer to be used to create net.Conn to connect to cluster address
	// in TCPNetwork.AddCluster. Set to use a custom dialer.
	Dialer *net.Dialer

	totalClients int64
	totalClosed  int64
	totalActive  int64
	totalOpened  int64

	// MaxConnection defines the total allowed connections for
	// giving network.
	MaxConnections int

	// MaxClusterRetries specifies allowed max retries used by the
	// RetryPolicy for reconnecting to a disconnected cluster network.
	MaxClusterRetries int

	// ClusterRetryDelay defines the duration used to expand for
	// each retry of reconnecting to a cluster address.
	ClusterRetryDelay time.Duration

	// MaxInfoWait defines the maximum duration allowed for a
	// waiting for MNET:CINFO response.
	MaxInfoWait time.Duration

	// MaxWriteDeadline defines max time before all clients collected writes must be written to the connection.
	MaxWriteDeadline time.Duration

	// MaxWriteSize sets given max size of buffer for client, each client writes collected
	// till flush must not exceed else will not be buffered and will be written directly.
	MaxWriteSize int

	raddr    net.Addr
	pool     chan func()
	cu       sync.RWMutex
	clients  map[string]*tcpServerClient
	ctx      context.Context
	routines sync.WaitGroup
}

// Start initializes the network listener.
func (n *TCPNetwork) Start(ctx context.Context) error {
	if n.Metrics == nil {
		n.Metrics = metrics.New()
	}

	if n.ID == "" {
		n.ID = uuid.NewV4().String()
	}

	n.Addr = netutils.GetAddr(n.Addr)
	if n.ServerName == "" {
		host, _, _ := net.SplitHostPort(n.Addr)
		n.ServerName = host
	}

	defer n.Metrics.Emit(
		metrics.Message("TCPNetwork.Start"),
		metrics.With("network", n.ID),
		metrics.With("addr", n.Addr),
		metrics.With("serverName", n.ServerName),
		metrics.WithID(n.ID),
	)

	if n.TLS != nil && !n.TLS.InsecureSkipVerify {
		n.TLS.ServerName = n.ServerName
	}

	stream, err := mlisten.Listen("tcp", n.Addr, n.TLS)
	if err != nil {
		return err
	}

	//n.ctx = ctx
	n.raddr = stream.Addr()
	n.pool = make(chan func(), 0)
	n.clients = make(map[string]*tcpServerClient)

	if n.MaxWriteSize <= 0 {
		n.MaxWriteSize = mnet.MaxBufferSize
	}

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

	if n.MaxWriteDeadline <= 0 {
		n.MaxWriteDeadline = mnet.MaxFlushDeadline
	}

	n.routines.Add(2)
	go n.runStream(stream)
	go n.handleClose(ctx, stream)

	return nil
}

func (n *TCPNetwork) handleClose(ctx context.Context, stream melon.ConnReadWriteCloser) {
	defer n.routines.Done()

	<-ctx.Done()

	n.cu.RLock()
	for _, conn := range n.clients {
		n.cu.RUnlock()
		conn.closeConnection(mnet.Client{})
		n.cu.RLock()
	}
	n.cu.RUnlock()

	if err := stream.Close(); err != nil {
		n.Metrics.Emit(
			metrics.Error(err),
			metrics.Message("TCPNetwork.endLogic"),
			metrics.With("network", n.ID),
			metrics.With("addr", n.Addr),
			metrics.With("serverName", n.ServerName),
			metrics.WithID(n.ID),
		)
	}
}

// Statistics returns statics associated with TCPNetwork.
func (n *TCPNetwork) Statistics() mnet.NetworkStatistic {
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
func (n *TCPNetwork) Wait() {
	n.routines.Wait()
}

func (n *TCPNetwork) getOtherClients(cm mnet.Client) ([]mnet.Client, error) {
	n.cu.Lock()
	defer n.cu.Unlock()

	var clients []mnet.Client
	for id, conn := range n.clients {
		if id == cm.ID {
			continue
		}

		var client mnet.Client
		client.NID = n.ID
		client.ID = conn.id
		client.Metrics = n.Metrics
		client.LiveFunc = conn.isLive
		client.InfoFunc = conn.getInfo
		client.FlushFunc = conn.flush
		client.WriteFunc = conn.write
		client.IsClusterFunc = conn.isCluster
		client.StatisticFunc = conn.getStatistics
		client.RemoteAddrFunc = conn.getRemoteAddr
		client.LocalAddrFunc = conn.getLocalAddr
		client.SiblingsFunc = n.getOtherClients
		clients = append(clients, client)
	}

	return clients, nil
}

var defaultDialer = &net.Dialer{Timeout: 2 * time.Second}

// AddCluster attempts to add new connection to another mnet tcp server.
// It will attempt to dial specified address returning an error if the
// giving address failed or was not able to meet the handshake protocols.
func (n *TCPNetwork) AddCluster(addr string) error {
	var err error
	var conn net.Conn

	if n.Dialer != nil {
		conn, err = n.Dialer.Dial("tcp", addr)
	} else {
		conn, err = defaultDialer.Dial("tcp", addr)
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

	return n.addClient(conn, policy, true)
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

func (n *TCPNetwork) addClient(conn net.Conn, policy mnet.RetryPolicy, isCluster bool) error {
	defer atomic.AddInt64(&n.totalOpened, -1)
	defer atomic.AddInt64(&n.totalClosed, 1)

	id := uuid.NewV4().String()

	client := mnet.Client{
		ID:      id,
		NID:     n.ID,
		Metrics: n.Metrics,
	}

	n.Metrics.Emit(
		metrics.WithID(n.ID),
		metrics.With("network", n.ID),
		metrics.With("client_id", id),
		metrics.With("network-addr", n.Addr),
		metrics.With("serverName", n.ServerName),
		metrics.Info("New Client Connection"),
		metrics.With("local_addr", conn.LocalAddr()),
		metrics.With("remote_addr", conn.RemoteAddr()),
	)

	nc := new(tcpServerClient)
	nc.id = id
	nc.nid = n.ID
	nc.conn = conn
	nc.network = n
	nc.metrics = n.Metrics
	nc.serverAddr = n.raddr
	nc.isACluster = isCluster
	nc.maxInfoWait = n.MaxInfoWait
	nc.localAddr = conn.LocalAddr()
	nc.remoteAddr = conn.RemoteAddr()
	nc.maxWrite = n.MaxWriteSize
	nc.sos = newGuardedBuffer(512)
	nc.maxDeadline = n.MaxWriteDeadline
	nc.parser = new(internal.TaggedMessages)
	nc.buffWriter = bufio.NewWriterSize(conn, n.MaxWriteSize)

	client.LiveFunc = nc.isLive
	client.InfoFunc = nc.getInfo
	client.ReaderFunc = nc.read
	client.WriteFunc = nc.write
	client.FlushFunc = nc.flush
	client.CloseFunc = nc.closeConn
	client.IsClusterFunc = nc.isCluster
	client.LocalAddrFunc = nc.getLocalAddr
	client.StatisticFunc = nc.getStatistics
	client.SiblingsFunc = n.getOtherClients
	client.RemoteAddrFunc = nc.getRemoteAddr

	nc.worker.Add(1)
	go nc.readLoop(client)

	if err := nc.handshake(client); err != nil {
		return err
	}

	n.cu.Lock()
	n.clients[id] = nc
	n.cu.Unlock()

	atomic.AddInt64(&n.totalClients, 1)
	go func() {
		if err := n.Handler(client); err != nil {
			client.Close()
			n.Metrics.Emit(
				metrics.Error(err),
				metrics.WithID(nc.id),
				metrics.With("network", n.ID),
				metrics.With("addr", nc.remoteAddr),
				metrics.Message("Connection Handler encountered error"),
			)
		}

		n.cu.Lock()
		delete(n.clients, id)
		n.cu.Unlock()

		if policy != nil {
			go func() {
				if err := policy.Retry(); err != nil {
					n.Metrics.Emit(
						metrics.Error(err),
						metrics.WithID(nc.id),
						metrics.With("network", n.ID),
						metrics.With("addr", nc.remoteAddr),
						metrics.Message("RetryPolicy failed"),
					)
				}
			}()
		}
	}()

	return nil
}

// runStream runs the process of listening for new connections and
// creating appropriate client objects which will handle behaviours
// appropriately.
func (n *TCPNetwork) runStream(stream melon.ConnReadWriteCloser) {
	defer n.routines.Done()

	defer n.Metrics.Emit(
		metrics.With("network", n.ID),
		metrics.Message("TCPNetwork.runStream"),
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
