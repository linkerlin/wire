package mudp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"time"

	"sync"

	"bufio"

	"io"

	"github.com/influx6/faux/metrics"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/faux/pools/done"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/internal"
	uuid "github.com/satori/go.uuid"
)

var (
	bufferPool = done.NewDonePool(218, 20)
)

// targetConn targets a giving connections net.Addr,
// using the underline udp connection.
type targetConn struct {
	target net.Addr
	client bool
	conn   *net.UDPConn
}

// Write implements the io.Writer logic.
func (t targetConn) Write(d []byte) (int, error) {
	if t.client {
		return t.conn.Write(d)
	}

	return t.conn.WriteTo(d, t.target)
}

type udpServerClient struct {
	totalRead      int64
	totalWritten   int64
	totalFlushOut  int64
	totalWriteMsgs int64
	totalReadMsgs  int64
	closedCounter  int64
	maxWrite       int
	maxDeadline    time.Duration
	id             string
	nid            string
	mainAddr       net.Addr
	localAddr      net.Addr
	remoteAddr     net.Addr
	metrics        metrics.Metrics
	parser         *internal.TaggedMessages
	network        *UDPNetwork
	bu             sync.Mutex
	buffer         *bufio.Writer
	cu             sync.Mutex
	conn           *net.UDPConn
}

func (nc *udpServerClient) getRemoteAddr(_ mnet.Client) (net.Addr, error) {
	return nc.remoteAddr, nil
}

func (nc *udpServerClient) getLocalAddr(_ mnet.Client) (net.Addr, error) {
	return nc.localAddr, nil
}

func (nc *udpServerClient) getStatistics(_ mnet.Client) (mnet.ClientStatistic, error) {
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

func (nc *udpServerClient) write(mn mnet.Client, size int) (io.WriteCloser, error) {
	if err := nc.isAlive(mn); err != nil {
		return nil, err
	}

	var conn *net.UDPConn
	nc.cu.Lock()
	conn = nc.conn
	nc.cu.Unlock()

	if conn == nil {
		return nil, mnet.ErrAlreadyClosed
	}

	return bufferPool.Get(size, func(d int, from io.WriterTo) error {
		atomic.AddInt64(&nc.totalWriteMsgs, 1)
		atomic.AddInt64(&nc.totalWritten, int64(d))

		nc.bu.Lock()
		defer nc.bu.Unlock()

		if nc.buffer == nil {
			return mnet.ErrAlreadyClosed
		}

		buffered := nc.buffer.Buffered()
		atomic.AddInt64(&nc.totalFlushOut, int64(buffered))

		// size of next write.
		toWrite := buffered + d

		// add size header
		toWrite += mnet.HeaderLength

		if toWrite >= nc.maxWrite {
			if err := nc.buffer.Flush(); err != nil {
				return err
			}
		}

		// write length header first.
		header := make([]byte, mnet.HeaderLength)
		binary.BigEndian.PutUint32(header, uint32(d))
		nc.buffer.Write(header)

		// then flush data alongside header.
		_, err := from.WriteTo(nc.buffer)
		return err
	}), nil
}

func (nc *udpServerClient) isAlive(_ mnet.Client) error {
	if atomic.LoadInt64(&nc.closedCounter) == 1 {
		return mnet.ErrAlreadyClosed
	}
	return nil
}

func (nc *udpServerClient) read(mn mnet.Client) ([]byte, error) {
	if err := nc.isAlive(mn); err != nil {
		return nil, err
	}

	atomic.AddInt64(&nc.totalReadMsgs, 1)
	indata, err := nc.parser.Next()
	atomic.AddInt64(&nc.totalRead, int64(len(indata)))
	return indata, err
}

func (nc *udpServerClient) flush(mn mnet.Client) error {
	if err := nc.isAlive(mn); err != nil {
		return err
	}

	if atomic.LoadInt64(&nc.closedCounter) == 1 {
		return mnet.ErrAlreadyClosed
	}

	var conn *net.UDPConn
	nc.cu.Lock()
	conn = nc.conn
	nc.cu.Unlock()

	if conn == nil {
		return mnet.ErrAlreadyClosed
	}

	nc.bu.Lock()
	defer nc.bu.Unlock()

	if nc.buffer.Buffered() != 0 {
		//conn.SetWriteDeadline(time.Now().Add(nc.maxDeadline))
		if err := nc.buffer.Flush(); err != nil {
			//conn.SetWriteDeadline(time.Time{})
			return err
		}
		//conn.SetWriteDeadline(time.Time{})
	}

	return nil
}

func (nc *udpServerClient) close(mn mnet.Client) error {
	if err := nc.isAlive(mn); err != nil {
		return err
	}

	atomic.StoreInt64(&nc.closedCounter, 1)

	nc.flush(mn)

	nc.cu.Lock()
	if nc.conn == nil {
		nc.cu.Unlock()
		return mnet.ErrAlreadyClosed
	}
	nc.cu.Unlock()

	if nc.network != nil {
		nc.network.cu.Lock()
		delete(nc.network.clients, nc.mainAddr.String())
		nc.network.cu.Unlock()
	}

	nc.cu.Lock()
	nc.conn = nil
	nc.cu.Unlock()

	nc.bu.Lock()
	nc.buffer = nil
	nc.bu.Unlock()

	return nil
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

	// MaxWriterSize sets the maximum size of bufio.Writer for
	// network connection.
	MaxWriterSize int

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
	clients map[string]*udpServerClient
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

// Start boots up the server and initializes all internals to make
// itself ready for servicing requests.
func (n *UDPNetwork) Start(ctx context.Context) error {
	if err := n.isAlive(); err == nil {
		return err
	}

	n.clients = make(map[string]*udpServerClient)

	if n.UDPNetwork == "" {
		n.UDPNetwork = "udp"
	}

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

	go func() {
		n.Wait()
		atomic.StoreInt64(&n.started, 0)
	}()

	return nil
}

func (n *UDPNetwork) handleCloseRequest(ctx context.Context, con *net.UDPConn) {
	defer n.rung.Done()
	<-ctx.Done()
	con.Close()
}

// Wait blocks the call till all go-routines created by network has shutdown.
func (n *UDPNetwork) Wait() {
	n.rung.Wait()
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
		//mclient.StreamFunc = client.stream
		mclient.ReaderFunc = client.read
		mclient.FlushFunc = client.flush
		mclient.LiveFunc = client.isAlive
		mclient.StatisticFunc = client.getStatistics
		mclient.LocalAddrFunc = client.getLocalAddr
		mclient.RemoteAddrFunc = client.getRemoteAddr
		mclient.SiblingsFunc = func(_ mnet.Client) ([]mnet.Client, error) {
			return n.getAllClient(client.localAddr), nil
		}

		clients = append(clients, mclient)
	}

	return clients
}

// addClient adds a new network connection into the network.
func (n *UDPNetwork) addClient(addr net.Addr, core *net.UDPConn) *udpServerClient {
	n.cu.Lock()
	defer n.cu.Unlock()

	client, ok := n.clients[addr.String()]
	if !ok {
		client = new(udpServerClient)
		client.nid = n.ID
		client.network = n
		client.conn = core
		client.mainAddr = addr
		client.remoteAddr = addr
		client.localAddr = n.laddr
		client.metrics = n.Metrics
		client.parser = new(internal.TaggedMessages)
		client.id = uuid.NewV4().String()
		client.maxWrite = n.MaxWriterSize
		client.maxDeadline = n.MaxWriteDeadline
		client.buffer = bufio.NewWriterSize(targetConn{
			conn:   core,
			target: addr,
		}, n.MaxWriterSize)

		// store client in map.
		n.clients[addr.String()] = client

		var mclient mnet.Client
		mclient.Metrics = n.Metrics
		mclient.NID = n.ID
		mclient.ID = client.id
		mclient.CloseFunc = client.close
		mclient.WriteFunc = client.write
		mclient.ReaderFunc = client.read
		mclient.FlushFunc = client.flush
		mclient.LiveFunc = client.isAlive
		mclient.StatisticFunc = client.getStatistics
		mclient.LiveFunc = client.isAlive
		mclient.LocalAddrFunc = client.getLocalAddr
		mclient.RemoteAddrFunc = client.getRemoteAddr
		mclient.SiblingsFunc = func(_ mnet.Client) ([]mnet.Client, error) {
			return n.getAllClient(addr), nil
		}

		n.rung.Add(1)
		go func(mc mnet.Client, addr net.Addr) {
			defer n.rung.Done()

			if err := n.Handler(mc); err != nil {
				atomic.StoreInt64(&client.closedCounter, 1)
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

	incoming := make([]byte, mnet.MinBufferSize)

	for {
		select {
		case <-ctx.Done():
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
						"addr": addr,
					},
				})

				continue
			}

			client := n.addClient(addr, conn)
			atomic.AddInt64(&client.totalRead, int64(nn))
			if err := client.parser.Parse(incoming[:nn]); err != nil {
				n.Metrics.Send(metrics.Entry{
					ID:      n.ID,
					Message: "client unable to handle message",
					Level:   metrics.ErrorLvl,
					Field: metrics.Field{
						"err":  err,
						"addr": addr,
						"data": string(incoming[:nn]),
					},
				})
				continue
			}

			// Lets resize buffer within area.
			//if nn < mnet.MinBufferSize {
			//	incoming = make([]byte, mnet.MinBufferSize/2)
			//	continue
			//}

			if nn > mnet.MinBufferSize && nn <= mnet.MinBufferSize*2 {
				incoming = make([]byte, mnet.MinBufferSize*2)
				continue
			}

			if nn > mnet.MinBufferSize && nn <= n.MaxWriterSize/2 {
				incoming = make([]byte, n.MaxWriterSize/2)
				continue
			}

			if nn > mnet.MinBufferSize && nn >= n.MaxWriterSize/2 {
				incoming = make([]byte, n.MaxWriterSize)
				continue
			}

			incoming = make([]byte, mnet.MinBufferSize)
		}
	}
}

func (n *UDPNetwork) handleCloseConnections(ctx context.Context) {
	stand := mnet.Client{}

	n.cu.RLock()
	for _, client := range n.clients {
		atomic.StoreInt64(&client.closedCounter, 1)
		client.close(stand)
	}
	n.cu.RUnlock()

	n.cu.Lock()
	n.clients = make(map[string]*udpServerClient)
	n.cu.Unlock()
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
