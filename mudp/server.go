package mudp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"sync"

	"bufio"

	"io"

	"github.com/influx6/faux/metrics"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/internal"
	"github.com/satori/go.uuid"
)

var (
	actionWriterPool = sync.Pool{
		New: func() interface{} {
			return new(internal.ActionLengthWriter)
		},
	}
)

type proxyWriter struct {
	target net.Addr
	conn   *net.UDPConn
}

func (pw proxyWriter) Write(d []byte) (int, error) {
	return pw.conn.WriteTo(d, pw.target)
}

// targetConn targets a giving connections net.Addr,
// using the underline udp connection.
type targetConn struct {
	id       string
	nid      string
	mainAddr net.Addr
	target   net.Addr
	raddr    net.Addr
	laddr    net.Addr
	metrics  metrics.Metrics
	parser   *internal.TaggedMessages

	maxWrite    int
	maxDeadline time.Duration

	totalRead     int64
	totalWritten  int64
	totalFlushed  int64
	totalMsgWrite int64
	totalMsgRead  int64
	reconnects    int64
	closed        int64

	br      *internal.LengthRecvReader
	bu      sync.Mutex
	bw      *bufio.Writer
	network *UDPNetwork
}

func (n *targetConn) readIncoming(r io.Reader) error {
	n.br.Reset(r)

	data := make([]byte, mnet.SmallestMinBufferSize)
	for {
		size, err := n.br.ReadHeader()
		if err != nil {
			if err != io.EOF {
				return err
			}

			return nil
		}

		data = make([]byte, size)
		_, err = n.br.Read(data)
		if err != nil {
			return err
		}

		if err = n.parser.Parse(data); err != nil {
			return err
		}
	}
}

// write returns an appropriate io.WriteCloser with appropriate size
// to contain data to be written to be connection.
func (n *targetConn) flush(cm mnet.Client) error {
	if err := n.network.isAlive(); err != nil {
		return err
	}

	var conn net.Conn
	n.network.mu.Lock()
	conn = n.network.conn
	n.network.mu.Unlock()

	if conn == nil {
		return mnet.ErrAlreadyClosed
	}

	n.bu.Lock()
	defer n.bu.Unlock()

	if n.bw == nil {
		return mnet.ErrAlreadyClosed
	}

	buffered := n.bw.Buffered()
	atomic.AddInt64(&n.totalFlushed, int64(buffered))

	if buffered > 0 {
		//conn.SetWriteDeadline(time.Now().Add(n.maxDeadline))
		if err := n.bw.Flush(); err != nil {
			//conn.SetWriteDeadline(time.Time{})
			return err
		}
		//conn.SetWriteDeadline(time.Time{})
	}

	return nil
}

func (n *targetConn) writeAction(size []byte, data []byte) error {
	atomic.AddInt64(&n.totalWritten, 1)
	atomic.AddInt64(&n.totalWritten, int64(len(data)))

	n.bu.Lock()
	defer n.bu.Unlock()

	if n.bw == nil {
		return mnet.ErrAlreadyClosed
	}

	available := n.bw.Available()
	buffered := n.bw.Buffered()
	atomic.AddInt64(&n.totalFlushed, int64(buffered))

	// size of next write.
	toWrite := buffered + len(data)

	// add size header
	toWrite += mnet.HeaderLength

	if toWrite > available {
		if err := n.bw.Flush(); err != nil {
			return err
		}
	}

	if _, err := n.bw.Write(size); err != nil {
		return err
	}

	if _, err := n.bw.Write(data); err != nil {
		return err
	}

	return nil
}

// write returns an appropriate io.WriteCloser with appropriate size
// to contain data to be written to be connection.
func (n *targetConn) write(cm mnet.Client, size int) (io.WriteCloser, error) {
	if err := n.isAlive(cm); err != nil {
		return nil, err
	}

	var conn net.Conn
	n.network.mu.Lock()
	conn = n.network.conn
	n.network.mu.Unlock()

	if conn == nil {
		return nil, mnet.ErrAlreadyClosed
	}

	newWriter := actionWriterPool.Get().(*internal.ActionLengthWriter)
	newWriter.Reset(func(size []byte, data []byte) error {
		err := n.writeAction(size, data)
		actionWriterPool.Put(newWriter)
		return err
	}, mnet.HeaderLength, size)

	return newWriter, nil
}

func (n *targetConn) localAddr(cm mnet.Client) (net.Addr, error) {
	return n.laddr, nil
}

func (n *targetConn) remoteAddr(cm mnet.Client) (net.Addr, error) {
	return n.raddr, nil
}

func (n *targetConn) read(cm mnet.Client) ([]byte, error) {
	if err := n.isAlive(cm); err != nil {
		return nil, err
	}

	atomic.AddInt64(&n.totalMsgRead, 1)
	indata, err := n.parser.Next()
	atomic.AddInt64(&n.totalRead, int64(len(indata)))
	return indata, err
}

func (n *targetConn) noop(cm mnet.Client) error {
	return nil
}

func (n *targetConn) isAlive(cm mnet.Client) error {
	if atomic.LoadInt64(&n.closed) == 1 {
		return mnet.ErrAlreadyClosed
	}
	return n.network.isAlive()
}

func (n *targetConn) close(cm mnet.Client) error {
	atomic.StoreInt64(&n.closed, 1)
	return nil
}

func (n *targetConn) stats(cm mnet.Client) (mnet.ClientStatistic, error) {
	var stats mnet.ClientStatistic
	stats.ID = n.id
	stats.Local = n.laddr
	stats.Remote = n.raddr
	stats.BytesRead = atomic.LoadInt64(&n.totalRead)
	stats.MessagesRead = atomic.LoadInt64(&n.totalMsgRead)
	stats.BytesWritten = atomic.LoadInt64(&n.totalWritten)
	stats.BytesFlushed = atomic.LoadInt64(&n.totalFlushed)
	stats.Reconnects = atomic.LoadInt64(&n.reconnects)
	stats.MessagesWritten = atomic.LoadInt64(&n.totalMsgWrite)
	return stats, nil
}

// UDPNetwork defines a network which runs ontop of provided mnet.ConnHandler.
type UDPNetwork struct {
	ID                 string
	Addr               string
	UDPNetwork         string
	ServerName         string
	Multicast          bool
	MulticastInterface *net.Interface
	Handler            mnet.ConnHandler
	Metrics            metrics.Metrics

	// ReadBuffer sets the read buffer to be used by the udp listener
	// ensure this is set according to what is set on os else the
	// udp listener will error out.
	ReadBuffer int

	// MaxWriterDeadline sets deadline to be enforced when writing
	// to network.
	MaxWriteDeadline time.Duration

	totalClients int64
	totalClosed  int64
	totalActive  int64
	totalOpened  int64
	started      int64

	ctx     context.Context
	addr    *net.UDPAddr
	raddr   net.Addr
	laddr   net.Addr
	rung    sync.WaitGroup
	cu      sync.RWMutex
	clients map[string]*targetConn
	mu      sync.Mutex
	conn    *net.UDPConn
}

func (n *UDPNetwork) isAlive() error {
	if atomic.LoadInt64(&n.started) == 0 {
		return errors.New("not started yet")
	}
	return nil
}

func (n *UDPNetwork) isActive(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	default:
		return true
	}
}

// Wait blocks the call till all go-routines created by network has shutdown.
func (n *UDPNetwork) Wait() {
	n.rung.Wait()
}

// Start boots up the server and initializes all internals to make
// itself ready for servicing requests.
func (n *UDPNetwork) Start(ctx context.Context) error {
	if err := n.isAlive(); err == nil {
		return err
	}

	n.clients = make(map[string]*targetConn)

	if n.UDPNetwork == "" {
		n.UDPNetwork = "udp"
	}

	if n.Metrics == nil {
		n.Metrics = metrics.New()
	}

	if n.ID == "" {
		n.ID = uuid.NewV4().String()
	}

	if n.MaxWriteDeadline <= 0 {
		n.MaxWriteDeadline = mnet.MaxFlushDeadline
	}

	n.Addr = netutils.GetAddr(n.Addr)
	if n.ServerName == "" {
		host, _, _ := net.SplitHostPort(n.Addr)
		n.ServerName = host
	}

	udpAddr, err := net.ResolveUDPAddr(n.UDPNetwork, n.Addr)
	if err != nil {
		return err
	}

	n.addr = udpAddr

	var serverConn *net.UDPConn
	if n.Multicast && n.MulticastInterface != nil {
		serverConn, err = net.ListenMulticastUDP(n.UDPNetwork, n.MulticastInterface, n.addr)
	} else {
		serverConn, err = net.ListenUDP(n.UDPNetwork, n.addr)
	}

	if err != nil {
		return err
	}

	if n.ReadBuffer != 0 {
		if err := serverConn.SetReadBuffer(n.ReadBuffer); err != nil {
			return err
		}
	}

	n.raddr = serverConn.RemoteAddr()
	n.laddr = serverConn.LocalAddr()

	n.mu.Lock()
	n.conn = serverConn
	n.mu.Unlock()

	n.ctx = ctx
	n.rung.Add(2)

	atomic.StoreInt64(&n.started, 1)
	go n.handleConnections(ctx, serverConn)
	go n.handleCloseRequest(ctx, serverConn)

	return nil
}

func (n *UDPNetwork) handleCloseRequest(ctx context.Context, con *net.UDPConn) {
	defer n.rung.Done()
	<-ctx.Done()
	con.Close()
}

func (n *UDPNetwork) getAllClient(skipAddr net.Addr) []mnet.Client {
	n.cu.RLock()
	defer n.cu.RUnlock()

	var clients []mnet.Client
	for _, client := range n.clients {
		if client.mainAddr == skipAddr {
			continue
		}
		var mclient mnet.Client
		mclient.NID = n.ID
		mclient.ID = client.id
		mclient.Metrics = n.Metrics
		mclient.WriteFunc = client.write
		mclient.FlushFunc = client.flush
		mclient.LiveFunc = client.isAlive
		mclient.StatisticFunc = client.stats
		mclient.LocalAddrFunc = client.localAddr
		mclient.RemoteAddrFunc = client.remoteAddr

		mclient.SiblingsFunc = func(_ mnet.Client) ([]mnet.Client, error) {
			return n.getAllClient(client.mainAddr), nil
		}

		clients = append(clients, mclient)
	}

	return clients
}

// addClient adds a new network connection into the network.
func (n *UDPNetwork) addClient(addr net.Addr, conn *net.UDPConn) *targetConn {
	n.cu.Lock()
	defer n.cu.Unlock()

	client, ok := n.clients[addr.String()]
	if !ok {
		client = new(targetConn)
		client.nid = n.ID
		client.network = n
		client.target = addr
		client.mainAddr = addr
		client.metrics = n.Metrics
		client.laddr = conn.LocalAddr()
		client.raddr = conn.RemoteAddr()
		client.id = uuid.NewV4().String()
		client.maxDeadline = n.MaxWriteDeadline
		client.parser = new(internal.TaggedMessages)
		client.br = internal.NewLengthRecvReader(nil, mnet.HeaderLength)
		client.bw = bufio.NewWriterSize(proxyWriter{conn: conn, target: addr}, mnet.MaxBufferSize)

		// store client in network clients map.
		n.clients[addr.String()] = client

		var mclient mnet.Client
		mclient.Metrics = n.Metrics
		mclient.NID = n.ID
		mclient.ID = client.id
		mclient.CloseFunc = client.close
		mclient.WriteFunc = client.write
		mclient.FlushFunc = client.flush
		mclient.ReaderFunc = client.read
		mclient.LiveFunc = client.isAlive
		mclient.StatisticFunc = client.stats
		mclient.LocalAddrFunc = client.localAddr
		mclient.RemoteAddrFunc = client.remoteAddr

		mclient.SiblingsFunc = func(_ mnet.Client) ([]mnet.Client, error) {
			return n.getAllClient(addr), nil
		}

		n.rung.Add(1)
		go func(mc mnet.Client, addr net.Addr) {
			defer atomic.StoreInt64(&client.closed, 1)
			defer n.rung.Done()

			if err := n.Handler(mc); err != nil {
				n.Metrics.Emit(
					metrics.Error(err),
					metrics.WithID(client.id),
					metrics.With("network", n.ID),
					metrics.Message("Connection Handler exiting"),
				)
			}

			n.cu.Lock()
			defer n.cu.Unlock()
			delete(n.clients, addr.String())
		}(mclient, addr)

	}

	return client
}

func (n *UDPNetwork) handleConnections(ctx context.Context, conn *net.UDPConn) {
	defer n.rung.Done()
	defer n.handleCloseConnections(ctx)
	defer atomic.StoreInt64(&n.started, 0)

	var tmpReader io.Reader
	incoming := make([]byte, mnet.MinBufferSize, mnet.MaxBufferSize)

	for {
		select {
		case <-ctx.Done():
			n.Metrics.Send(metrics.Entry{
				ID:      n.ID,
				Message: "closing network read loop",
				Level:   metrics.ErrorLvl,
				Field: metrics.Field{
					"addr": n.addr,
				},
			})
			return
		default:
			nn, addr, err := conn.ReadFrom(incoming)
			if err != nil {
				n.Metrics.Send(metrics.Entry{
					ID:      n.ID,
					Message: "failed to read message from connection",
					Level:   metrics.ErrorLvl,
					Field: metrics.Field{
						"err":  err,
						"addr": n.addr,
					},
				})

				continue
			}

			n.Metrics.Send(metrics.Entry{
				ID:      n.ID,
				Message: "Received new message",
				Level:   metrics.InfoLvl,
				Field: metrics.Field{
					"addr":   addr,
					"length": nn,
					"data":   string(incoming[:nn]),
				},
			})

			client := n.addClient(addr, conn)
			atomic.AddInt64(&client.totalRead, int64(nn))

			tmpReader = bytes.NewReader(incoming[:nn])
			if err := client.readIncoming(tmpReader); err != nil {
				n.Metrics.Send(metrics.Entry{
					ID:      n.ID,
					Message: "client unable to process message",
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

			if nn > len(incoming) && len(incoming) > mnet.MinBufferSize && nn <= mnet.MaxBufferSize/2 {
				incoming = incoming[0 : mnet.MaxBufferSize/2]
			}
		}
	}
}

func (n *UDPNetwork) handleCloseConnections(ctx context.Context) {
	n.cu.Lock()
	defer n.cu.Unlock()

	for _, client := range n.clients {
		atomic.StoreInt64(&client.closed, 1)
	}

	n.clients = make(map[string]*targetConn)
}

// Statistics returns statics associated with UDPNetwork.
func (n *UDPNetwork) Statistics() mnet.NetworkStatistic {
	var stats mnet.NetworkStatistic
	stats.ID = n.ID
	stats.LocalAddr = n.laddr
	stats.RemoteAddr = n.raddr
	stats.TotalClients = atomic.LoadInt64(&n.totalClients)
	stats.TotalClosed = atomic.LoadInt64(&n.totalClosed)
	stats.TotalOpened = atomic.LoadInt64(&n.totalOpened)
	return stats
}
