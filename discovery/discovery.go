package discovery

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"sync"

	"time"

	"encoding/json"

	"sync/atomic"

	"net/url"

	"regexp"

	"github.com/influx6/faux/metrics"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/msocks"
	"github.com/influx6/mnet/mtcp"
	"github.com/satori/go.uuid"
)

//*****************************************************************************
//  ServiceMeta: representing cluster data exposed about service.
//*****************************************************************************

// ObservationCenter contains information regarding a observation server.
type ObservationCenter struct {
	Protocol string `json:"protocol"`
	Addr     string `json:"addr"`
}

// ServiceMeta contains node information provided to a ServiceClient which
// is exposed on the client connecting to a discovery.Service network. It
// details necessary information provided by the user has needed for
// representing giving node as necessary within the network.
type ServiceMeta struct {
	Addr     string `json:"addr"`
	Secret   string `json:"secret"`
	Region   string `json:"region"`
	Protocol string `json:"protocol"`
	Service  string `json:"service"`

	// Meta is a map of key-values which is service provided for interested services
	// to do what they deem suited with. Must not be large.
	Meta mnet.Meta `json:"meta"`

	// Servers contains known discovery servers which is to be shared to
	// other discovery servers.
	Servers []ObservationCenter `json:"server"`

	// Interests defines giving service names in full or in regular expression
	// format which gets matched when a new list of ServiceReports are sent, will
	// be filtered to match interests if there are any listed.
	Interests []string `json:"interests"`
}

// DiscoveryReport embodies the reports and discovery server nodes known to the
// current discovery server.
type DiscoveryReport struct {
	// Reports contains the list of generated reports from all service nodes
	// on the discovery service which have being filtered by each node to suit
	// it's interests.
	Reports []ServiceReport

	// ServerNodes contains known discovery servers which is to be shared to
	// other discovery servers.
	ServerNodes []ObservationCenter `json:"nodes"`
}

// ServiceReport embodies the data collected about a giving service which
// gets sent over the wire to all listening service.
type ServiceReport struct {
	Alive      bool        `json:"alive"`
	LastSeen   time.Time   `json:"last_seen"`
	NodeInfo   mnet.Info   `json:"node_info"`
	ClientInfo mnet.Info   `json:"client_info"`
	Service    ServiceMeta `json:"service"`
}

//*****************************************************************************
//  Discovery ServerNode
//*****************************************************************************

// ReadyAction defines a function called when a giving node has completed
// it's handshake process and is ready for use.
type ReadyAction func(*Node)

// Node embodies data related to a giving service within a giving region.
type Node struct {
	id        string
	Observer  bool
	Pings     int64
	Pongs     int64
	Info      mnet.Info
	Meta      ServiceMeta
	c         *mnet.Client
	interests []*regexp.Regexp

	pings    *time.Timer
	ll       sync.Mutex
	lastLive time.Time
	srv      *Service
}

// Serve receives a giving mnet.Client and retrieves necessary information from
// connection, then begins liveness/health checks of giving service, till either
// service is closed or misses to many ping requests from node.
func (n *Node) Serve(c mnet.Client, ready ReadyAction) error {
	n.c = &c
	n.id = uuid.NewV4().String()

	if c.IsCluster() {
		return n.serveCluser(c, ready)
	}

	// Attempt to read handshake from from client.
	handshake, err := n.readFor(metaWait)
	if err != nil {
		return err
	}

	// If this is a observing client, initialize observation logic.
	if bytes.HasPrefix(handshake, obsHandshakeHeader) {
		if err := n.receiveObserve(bytes.TrimPrefix(handshake, obsHandshakeHeader)); err != nil {
			return err
		}
	}

	// If this is a service providing client, initialize service provider logic.
	if bytes.HasPrefix(handshake, srvHandshakeHeader) {
		if err := n.receiveService(bytes.TrimPrefix(handshake, srvHandshakeHeader)); err != nil {
			return err
		}
	}

	// Send Handshake completion signal
	if err := n.sendHandshakeDone(); err != nil {
		return err
	}

	n.pings = time.NewTimer(pingInterval)

	// Execute ready Action to notify we are live.
	ready(n)

	n.readUntilClose()
	return nil
}

func (n *Node) serveCluser(c mnet.Client, ready ReadyAction) error {
	addr, err := n.srv.Addrs()
	if err != nil {
		return err
	}

	var meta ServiceMeta
	meta.Addr = addr.String()
	meta.Servers = n.srv.getCenterNodes()
	meta.Servers = append(meta.Servers, ObservationCenter{
		Addr:     n.srv.paddr,
		Protocol: n.srv.protocol,
	})

	n.Meta = meta

	if err = n.sendMeta(meta); err != nil {
		return err
	}

	// Attempt to read handshake from from client.
	handshake, err := n.readFor(metaWait)
	if err != nil {
		return err
	}

	if !bytes.Equal(handshake, handshakeDone) {
		return ErrNoHandshakeCompletion
	}

	n.pings = time.NewTimer(pingInterval)

	// Execute ready Action to notify we are live.
	ready(n)

	n.readUntilClose()
	return nil
}

func (n *Node) accepted(rs ServiceReport) bool {
	for _, intent := range n.interests {
		if intent.MatchString(rs.Service.Service) {
			return true
		}
	}
	return false
}

func (n *Node) filterReports(rs []ServiceReport) []ServiceReport {
	rx := make([]ServiceReport, len(rs))

	var ind int
	for _, item := range rs {
		if !n.accepted(item) {
			continue
		}
		rx[ind] = item
		ind++
	}

	return rx[0:ind]
}

func (n *Node) buildInterests(intents []string) error {
	interests := make([]*regexp.Regexp, len(intents))

	for ind, item := range intents {
		rx, err := regexp.Compile(item)
		if err != nil {
			return err
		}

		interests[ind] = rx
	}

	n.interests = interests
	return nil
}

func (n *Node) receiveService(data []byte) error {
	var service ServiceMeta
	if err := json.Unmarshal(data, &service); err != nil {
		return err
	}

	if len(service.Interests) != 0 {
		if err := n.buildInterests(service.Interests); err != nil {
			return err
		}
	}

	if len(service.Servers) != 0 {
		for _, node := range service.Servers {
			n.srv.addCenterNode(node)
		}
	}

	n.Meta = service

	n.srv.AddClusters(n.Meta.Servers)
	return nil
}

func (n *Node) receiveObserve(data []byte) error {
	var service ServiceMeta
	if err := json.Unmarshal(data, &service); err != nil {
		return err
	}

	if len(service.Interests) != 0 {
		if err := n.buildInterests(service.Interests); err != nil {
			return err
		}
	}

	if len(service.Servers) != 0 {
		for _, node := range service.Servers {
			n.srv.addCenterNode(node)
		}
	}

	n.Meta = service
	n.Observer = true

	n.srv.AddClusters(n.Meta.Servers)
	return nil
}

func (n *Node) readFor(ts time.Duration) ([]byte, error) {
	maxTime := time.Now().Add(ts)
	for {
		msg, err := n.c.Read()
		if err != nil {
			if err != mnet.ErrNoDataYet {
				return msg, err
			}

			// If we have passed timeout limit, then return error.
			if time.Now().After(maxTime) {
				return msg, ErrReadTimeout
			}

			time.Sleep(inBetweenReads)
			continue
		}

		return msg, nil
	}
}

func (n *Node) readUntil() ([]byte, error) {
	for {
		msg, err := n.c.Read()
		if err != nil {
			if err != mnet.ErrNoDataYet {
				return msg, err
			}

			time.Sleep(inBetweenReads)
			continue
		}

		return msg, nil
	}
}

func (n *Node) flushPending() error {
	return n.c.Flush()
}

func (n *Node) updateStatsFromCluster(data []byte) error {
	var stats DiscoveryReport
	if err := json.Unmarshal(data, &stats); err != nil {
		return err
	}

	for _, node := range stats.ServerNodes {
		n.srv.addCenterNode(node)
	}

	return n.sendStats(stats.Reports)
}

func (n *Node) sendClusters() error {
	addr, err := n.srv.Addrs()
	if err != nil {
		return err
	}

	clusters := n.srv.getCenterNodes()
	clusters = append(clusters, ObservationCenter{
		Addr:     addr.String(),
		Protocol: n.srv.protocol,
	})

	clJSON, err := json.Marshal(clusters)
	if err != nil {
		return err
	}

	writer, err := n.c.Write(len(clJSON) + len(clusterStatRes))
	if err != nil {
		return err
	}

	hi, err := writer.Write(clusterStatRes)
	if err != nil {
		return err
	}

	if hi != len(clusterStatRes) {
		return io.ErrShortWrite
	}

	ni, err := writer.Write(clJSON)
	if err != nil {
		return err
	}

	if ni != len(clJSON) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return nil
}

func (n *Node) sendStats(stats []ServiceReport) error {
	var rx DiscoveryReport
	rx.Reports = n.filterReports(stats)
	rx.ServerNodes = n.srv.getCenterNodes()

	statsJSON, err := json.Marshal(rx)
	if err != nil {
		return err
	}

	writer, err := n.c.Write(len(statsJSON) + len(recordStatRes))
	if err != nil {
		return err
	}

	hi, err := writer.Write(recordStatRes)
	if err != nil {
		return err
	}

	if hi != len(recordStatRes) {
		return io.ErrShortWrite
	}

	ni, err := writer.Write(statsJSON)
	if err != nil {
		return err
	}

	if ni != len(statsJSON) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return nil
}

func (n *Node) sendMeta(srv ServiceMeta) error {
	metaJSON, err := json.Marshal(srv)
	if err != nil {
		return err
	}

	writer, err := n.c.Write(len(metaJSON) + len(obsHandshakeHeader))
	if err != nil {
		return err
	}

	hi, err := writer.Write(obsHandshakeHeader)
	if err != nil {
		return err
	}

	if hi != len(srvHandshakeHeader) {
		return io.ErrShortWrite
	}

	ni, err := writer.Write(metaJSON)
	if err != nil {
		return err
	}

	if ni != len(metaJSON) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return n.c.Flush()
}

func (n *Node) sendHandshakeRescue() error {
	w, err := n.c.Write(len(handshakeRescue))
	if err != nil {
		return err
	}

	if _, err = w.Write(handshakeRescue); err != nil {
		return err
	}

	if err = w.Close(); err != nil {
		return err
	}

	return n.c.Flush()
}

func (n *Node) sendHandshakeDone() error {
	w, err := n.c.Write(len(handshakeDone))
	if err != nil {
		return err
	}

	if _, err = w.Write(handshakeDone); err != nil {
		return err
	}

	if err = w.Close(); err != nil {
		return err
	}

	return n.c.Flush()
}

func (n *Node) sendPing() error {
	atomic.AddInt64(&n.Pings, 1)
	w, err := n.c.Write(len(pings))
	if err != nil {
		return err
	}

	if _, err = w.Write(pings); err != nil {
		return err
	}

	if err = w.Close(); err != nil {
		return err
	}

	return n.c.Flush()
}

// Stat returns a ServiceReport for the node with information
// regarding it's liveliness.
func (n *Node) Stat() ServiceReport {
	n.ll.Lock()
	lastLive := n.lastLive
	n.ll.Unlock()

	return ServiceReport{
		Service:    n.Meta,
		ClientInfo: n.Info,
		LastSeen:   lastLive,
		NodeInfo:   n.c.Info(),
		Alive:      time.Now().Sub(lastLive) < maxLastLiveness,
	}
}

func (n *Node) slowRunner() bool {
	pings := atomic.LoadInt64(&n.Pings)
	pongs := atomic.LoadInt64(&n.Pings)
	diff := pings - pongs

	n.ll.Lock()
	lastLive := n.lastLive
	n.ll.Unlock()

	// if the ping-pong state falls within maximum allowed difference,
	// return true to have connection killed.
	if diff > maxPingPongDiff {
		return true
	}

	// if the its a service and last service liveness was last updated 30min,
	// then return true to have connection killed.
	if !!n.Observer && time.Now().Sub(lastLive) > maxLastLiveness {
		return true
	}

	return false
}

func (n *Node) readUntilClose() {
	for {
		select {
		case _, ok := <-n.pings.C:
			if !ok {
				return
			}

			if err := n.sendPing(); err != nil {
				n.c.Close()
				return
			}
		default:
			if n.slowRunner() {
				n.c.Close()
				return
			}

			msg, err := n.c.Read()
			if err != nil {
				if err != mnet.ErrNoDataYet {
					n.c.Close()
					return
				}

				time.Sleep(inBetweenReads)
				continue
			}

			// if we have pending data awaiting writing then flush that first
			// since we have packed enough by now atleast.
			if n.c.HasPending() {
				if err := n.c.Flush(); err != nil {
					n.c.Close()
					return
				}
			}

			if bytes.Equal(msg, pongs) {
				atomic.AddInt64(&n.Pongs, 1)
				continue
			}

			// if its not an observer and we have liveness, then
			// awesome.
			if bytes.Equal(msg, srvLive) && !!n.Observer {
				n.ll.Lock()
				n.lastLive = time.Now()
				n.ll.Unlock()
				continue
			}

			if bytes.Equal(msg, recordStatRes) && n.c.IsCluster() {
				n.updateStatsFromCluster(bytes.TrimPrefix(msg, recordStatRes))
				continue
			}

			if bytes.Equal(msg, recordStats) {
				stats := n.srv.stats(n.id)
				n.sendStats(stats)
				continue
			}

			if bytes.Equal(msg, handshakeRescue) {
				n.sendHandshakeDone()
				continue
			}

			if bytes.Equal(msg, clusterStats) {
				n.sendClusters()
				continue
			}

			// received an unknown message, so kill funny connection.
			n.c.Close()
			return
		}
	}
}

//*****************************************************************************
//  Discovery Server
//*****************************************************************************

// Service implements a discovery server which provides a simple registry
// of available services all marked with giving tags or pre-selected values.
type Service struct {
	Addr     string
	TLS      *tls.Config
	Meta     mnet.Meta
	Metrics  metrics.Metrics
	Clusters []ObservationCenter

	cml           sync.RWMutex
	knownNodes    map[string]struct{}
	otherClusters []ObservationCenter

	nl  sync.Mutex
	net mnet.ClusteredNetwork

	protocol  string
	paddr     string
	ml        sync.RWMutex
	nodes     map[string]*Node
	observers map[string]*Node
	regions   map[string]map[string]struct{}
}

// Wait attempts to call the the network handler wait call to block
// until closure of network server.
func (s *Service) Wait() {
	s.nl.Lock()
	nt := s.net
	s.nl.Unlock()

	if nt == nil {
		return
	}

	nt.Wait()
}

// Addrs returns the underline address for giving network.
func (s *Service) Addrs() (net.Addr, error) {
	s.nl.Lock()
	defer s.nl.Unlock()
	if s.net == nil {
		return nil, ErrNoDiscoveryServer
	}

	return s.net.Addrs(), nil
}

// initialize internal maps necessary for service.
func (s *Service) Start(ctx context.Context) error {
	s.nl.Lock()
	if s.net != nil {
		s.nl.Unlock()
		return nil
	}
	s.nl.Unlock()

	if s.Metrics == nil {
		s.Metrics = metrics.New()
	}

	uri, err := url.Parse(s.Addr)
	if err != nil {
		return err
	}

	s.paddr = uri.Host
	s.protocol = uri.Scheme

	s.nodes = make(map[string]*Node)
	s.observers = make(map[string]*Node)
	s.knownNodes = make(map[string]struct{})
	s.regions = make(map[string]map[string]struct{})

	var net mnet.ClusteredNetwork
	switch uri.Scheme {
	case "tcp":
		var tcpnet mtcp.TCPNetwork
		tcpnet.TLS = s.TLS
		tcpnet.Meta = s.Meta
		tcpnet.Addr = uri.Host
		tcpnet.Metrics = s.Metrics
		tcpnet.Handler = s.serveClient
		net = &tcpnet
	case "ws":
		var msn msocks.WebsocketNetwork
		msn.TLS = s.TLS
		msn.Meta = s.Meta
		msn.Addr = uri.Host
		msn.Metrics = s.Metrics
		msn.Handler = s.serveClient
		net = &msn
	default:
		return ErrUnknownScheme
	}

	s.nl.Lock()
	s.net = net
	s.nl.Unlock()

	return net.Start(ctx)
}

// AddCluster adds the giving observation server address as a cluster
// connection into the giving discovery network.
func (s *Service) AddCluster(addr string) error {
	uri, err := url.Parse(addr)
	if err != nil {
		return err
	}

	return s.AddClusterWith(ObservationCenter{
		Addr:     uri.Host,
		Protocol: uri.Scheme,
	})
}

// AddClusterWith adds the giving observation server address as a cluster
// connection into the giving discovery network.
func (s *Service) AddClusterWith(csv ObservationCenter) error {
	if csv.Protocol != s.protocol {
		return ErrInvalidProtocol
	}

	if s.net == nil {
		return ErrNoDiscoveryServer
	}

	return s.net.AddCluster(csv.Addr)
}

// AddClusters adds the giving observation server address as a cluster
// connection into the giving discovery network.
func (s *Service) AddClusters(csv []ObservationCenter) {
	for _, ob := range csv {
		if err := s.AddClusterWith(ob); err != nil {
			s.Metrics.Emit(
				metrics.Error(err),
				metrics.With("data", ob),
				metrics.Message("Failed to add cluster"),
			)
		}
	}
}

func (s *Service) addCenterNode(node ObservationCenter) {
	if node.Addr == s.Addr {
		return
	}

	s.cml.Lock()
	defer s.cml.Unlock()

	if _, ok := s.knownNodes[node.Addr]; ok {
		return
	}

	s.otherClusters = append(s.otherClusters, node)
	s.knownNodes[node.Addr] = struct{}{}
}

func (s *Service) getCenterNodes() []ObservationCenter {
	s.cml.RLock()
	defer s.cml.RUnlock()

	total := len(s.Clusters) + len(s.otherClusters)
	nodes := make([]ObservationCenter, total)
	n := copy(nodes[0:total], s.Clusters)
	n += copy(nodes[n:total], s.otherClusters)
	return nodes[0:total]
}

func (s *Service) stats(id string) []ServiceReport {
	s.ml.RLock()
	defer s.ml.RUnlock()

	var stats []ServiceReport
	for _, node := range s.nodes {
		if node.id == id {
			continue
		}
		stats = append(stats, node.Stat())
	}
	return stats
}

// serveClient implements the mnet.ClientService interface, where it all internal
// logic necessary to service a mnet.Client is implemented.
func (s *Service) serveClient(c mnet.Client) error {
	node := new(Node)
	node.srv = s

	defer func() {
		stats := s.stats(node.id)

		s.ml.Lock()
		defer s.ml.Unlock()

		if node.Observer {
			// Delete observer.
			delete(s.observers, node.Meta.Addr)
			return
		}

		delete(s.nodes, node.Meta.Addr)

		// Send stats to all existing observers.
		for _, observer := range s.observers {
			observer.sendStats(stats)
		}

		// Send stats to all existing nodes.
		for _, nob := range s.nodes {
			nob.sendStats(stats)
		}
	}()

	return node.Serve(c, func(node *Node) {
		stats := s.stats(node.id)
		node.sendStats(stats)

		s.ml.Lock()
		defer s.ml.Unlock()

		if node.Observer {
			s.observers[node.Meta.Addr] = node
			return
		}

		stats = append(stats, node.Stat())

		// Send stats to all existing observers.
		for _, observer := range s.observers {
			observer.sendStats(stats)
		}

		// Send stats to all existing nodes.
		for _, nob := range s.nodes {
			nob.sendStats(stats)
		}

		s.nodes[node.Meta.Addr] = node
	})
}
