package discovery

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sync"
	"time"

	"bytes"

	"sync/atomic"

	"github.com/gokit/history"
	"github.com/influx6/mnet"
	"github.com/influx6/mnet/msocks"
	"github.com/influx6/mnet/mtcp"
)

// errors ...
var (
	ErrNoNodeResponseRecv    = errors.New("failed to receive cluster nodes")
	ErrNoHandshakeCompletion = errors.New("failed to receive handshake completion signal")
)

//*****************************************************************************
//  Node Types
//*****************************************************************************

// NodeType defines an int32 value to represent a giving
// desired node type.
type NodeType int32

// constants of NodeTypes.
const (
	BadNode NodeType = iota
	ServiceNode
	ObservingNode
)

// String returns giving string name of node type.
func (n NodeType) String() string {
	switch n {
	case ServiceNode:
		return "ServiceNode"
	case ObservingNode:
		return "ObservingNode"
	}
	return "UnknownNode"
}

//*****************************************************************************
//  Event Function Types
//*****************************************************************************

// StatsFn defines a function type which receives new service report information.
type StatsFn func([]ServiceReport)

//*****************************************************************************
//  DiscoveryNode: Service Intent
//*****************************************************************************

// Intent embodies a description provided to a discoveryAgent to describe said
// service it is exposing to the discovery server.
type Intent struct {
	// Type specifies the giving node type to be created on a discovery agent.
	Type NodeType

	// Region specifies the desired region of service.
	Region string

	// TLS provides giving tls configuration to be used in connecting with
	// a mtcp/tcp or msocks/websocket client.
	TLS *tls.Config

	// Secret specifies the public secret that another service is aware of
	// that can be used to identify if giving service is part of it's group
	// or not. It should not store anything that is really important, and is
	// more like a group identifier known only to a set of services who are
	// aware of it's validity.
	Secret string

	// Service specifies the name of service which other service can
	// use to identify said service.
	Service string

	// ServiceAddr defines giving address to the service being advertised to
	// the discovery server.
	ServiceAddr string

	// ServerAddr defines giving address to initially connect to available
	// discovery server.
	ServerAddr string

	// Meta is a map of key-values which is service provided for interested services
	// to do what they deem suited with. Must not be large.
	Meta mnet.Meta

	// Interests defines giving service names in full or in regular expression
	// format which gets matched when a new list of ServiceReports are sent, will
	// be filtered to match interests if there are any listed.
	Interests []string

	// Fn defines function called for every update of service stats retrieved from
	// a discovery server.
	Fn StatsFn
}

// Validate returns an error if required parameters are empty or in an invalid state.
func (in *Intent) Validate() error {
	switch in.Type {
	case ObservingNode:
		return in.validateWhenObserving()
	case ServiceNode:
		return in.validateWhenService()
	default:
		return ErrUnknownNodeType
	}

	if in.Fn == nil {
		return ErrNoFn
	}

	return nil
}

func (in *Intent) validateWhenObserving() error {
	in.Service = ""
	in.ServiceAddr = ""

	if in.Region == "" {
		return ErrNoRegion
	}

	if in.ServerAddr == "" {
		return ErrNoServerAddr
	}

	return nil
}

func (in *Intent) validateWhenService() error {
	if in.Service == "" {
		return ErrNoService
	}

	if in.Region == "" {
		return ErrNoRegion
	}

	if in.ServiceAddr == "" {
		return ErrNoServiceAddr
	}

	if in.ServerAddr == "" {
		return ErrNoServerAddr
	}

	return nil
}

//*****************************************************************************
//  AgentNode
//*****************************************************************************

// Health defines a dataset which represents information
// sent over the wire related to a giving connection.
type Health struct {
	Seen     time.Duration
	Expected time.Duration
	Meta     map[string]interface{}
}

// AgentNode defines an interface which exposes methods to listen and
// response to health checks and disconnects.
type AgentNode interface {
	// SendHealth delivers latest health stats after receiving health signal.
	SendHealth(Health) error

	// Health returns a channel which signals the requests of new health stats
	// to be delivered to all listeners.
	Health() chan struct{}

	// CloseNotifier provides a channel which signals to the listener to
	// of an agent about the closure of a agent when it's Stop method is called.
	CloseNotifier() chan struct{}

	// Disconnects returns a channel to signal a disconnect of the agent internal
	// connection. This allows user to decide how to deal with agent if after disconnect
	// to be close or to attempt reconnection with the Reconnect() method.
	Disconnects() chan struct{}

	// Reconnect provides a mean to issue a reconnection to the discovery server
	// by the agent when a disconnect signal is received. It returns any encountered
	// error.
	Reconnect() error

	// Stop ends the agent internal connection and operation and closes the agent.
	Stop() error
}

// Declare returns a new instance of a discoveryAgent with provided details
// which will be shared with the discovery server on the discovery address.
func Agent(intent Intent) (AgentNode, error) {
	if err := intent.Validate(); err != nil {
		return nil, err
	}

	srv := &discoveryAgent{
		intent:  intent,
		obKnown: make(map[string]struct{}),
		stats:   time.NewTimer(statsInterval),
	}

	return srv, srv.serve()
}

//*****************************************************************************
//  discoveryNode Implementation
//*****************************************************************************

// discoveryAgent implements the client agent implementation built on the
type discoveryAgent struct {
	intent Intent
	stats  *time.Timer

	logs history.Ctx

	chinitd       bool
	healtChan     chan struct{}
	reconnectChan chan struct{}
	closedChan    chan struct{}

	initd     bool
	obl       sync.Mutex
	obKnown   map[string]struct{}
	observers []ObservationCenter

	closed      int64
	disconneted int64
	cl          sync.Mutex
	protocol    string
	srv         ServiceMeta
	c           *mnet.Client
}

// CloseNotifier returns a channel which is used to notify users of the close
// of the agent, this is usually done when the AgentNode.Stop() is called.
func (srv *discoveryAgent) CloseNotifier() chan struct{} {
	return srv.closedChan
}

// Health returns a channel which is used to signal for need of health
// information regarding service. This is usually working when agent is
// a service agent and not a observer.
func (srv *discoveryAgent) Health() chan struct{} {
	return srv.healtChan
}

// Disconnects returns a channel which signals a disconnect or failure to connect
// to discovery server to allow user halt or take appropriate action.s
func (srv *discoveryAgent) Disconnects() chan struct{} {
	return srv.reconnectChan
}

// SendHealth delivers giving health report for agent to the connected
// discovery server.
func (srv *discoveryAgent) SendHealth(h Health) error {
	hson, err := json.Marshal(h)
	if err != nil {
		return err
	}

	writer, err := srv.c.Write(len(hson) + len(healthRes))
	if err != nil {
		return err
	}

	hi, err := writer.Write(healthRes)
	if err != nil {
		return err
	}

	if hi != len(healthRes) {
		return io.ErrShortWrite
	}

	ni, err := writer.Write(hson)
	if err != nil {
		return err
	}

	if ni != len(hson) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return srv.c.Flush()
}

// Reconnect attempts to reconnect the agent to the underline transport.
// It returns any appropriate health.
func (srv *discoveryAgent) Reconnect() error {
	if !srv.isDisconnected() {
		return nil
	}

	return srv.serve()
}

// Stop closes the internal connection and ensures no more
// reconnection attempts are tried.
func (srv *discoveryAgent) Stop() error {
	if srv.isClosed() {
		return nil
	}

	defer atomic.StoreInt64(&srv.closed, 1)

	srv.cl.Lock()
	defer srv.cl.Unlock()

	err := srv.c.Close()
	close(srv.closedChan)
	return err
}

func (srv *discoveryAgent) isClosed() bool {
	return atomic.LoadInt64(&srv.closed) > 0
}

func (srv *discoveryAgent) isDisconnected() bool {
	return atomic.LoadInt64(&srv.disconneted) > 0
}

func (srv *discoveryAgent) serve() error {
	srv.logs = history.WithFields(history.Attrs{
		"intent":         srv.intent,
		"agent.protocol": srv.intent.Type.String(),
	})

	logctx := srv.logs.WithTitle("discoveryAgent.serve")

	if srv.isClosed() {
		logctx.Error(mnet.ErrAlreadyClosed, "discovery agent has being closed")
		return mnet.ErrAlreadyClosed
	}

	atomic.StoreInt64(&srv.disconneted, 0)
	atomic.StoreInt64(&srv.closed, 0)

	if !srv.chinitd {
		logctx.Info("initializing discoveryAgent channels")

		srv.chinitd = true
		srv.healtChan = make(chan struct{})
		srv.closedChan = make(chan struct{})
		srv.reconnectChan = make(chan struct{})
	}

	logctx.Info("connect to discovery service server")

	if err := srv.connectToServer(); err != nil {
		logctx.Error(err, "failed to connect to server")

		go func() {
			if srv.isClosed() {
				return
			}

			select {
			case <-srv.closedChan:
				return
			case srv.reconnectChan <- struct{}{}:
				return
			}
		}()

		return err
	}

	logctx.Info("node initiating handshake policy process")
	if err := srv.handshake(); err != nil {
		logctx.Error(err, "failed to finish handshake policy for discovery agent")

		go func() {
			if srv.isClosed() {
				return
			}

			select {
			case <-srv.closedChan:
				return
			case srv.reconnectChan <- struct{}{}:
				return
			}
		}()
		return err
	}

	go func() {
		defer func() {
			select {
			case <-srv.closedChan:
				return
			case srv.reconnectChan <- struct{}{}:
				return
			}
		}()

		logctx.Info("discovery agent read cycle has begun")
		srv.readUntilClose()
		logctx.Info("discovery agent read cycle ended")
	}()

	logctx.Info("discovery agent is ready")
	return nil
}

func (srv *discoveryAgent) connectToServer() error {

	srv.cl.Lock()
	c := srv.c
	srv.cl.Unlock()

	if c == nil {
		return srv.connectToServerAddr(srv.intent.ServerAddr)
	}

	srv.obl.Lock()
	nodes := srv.observers
	srv.obl.Unlock()

	c.Close()

	if len(nodes) == 1 {
		return c.Reconnect("")
	}

	item := nodes[0]
	nodes[0] = nodes[len(nodes)-1]

	logs := srv.logs.With("node", item)

	logs.Info("selected next node for server")

	if err := srv.connectToServerAddr(item.Addr); err != nil {
		srv.logs.Error(err, "failed to connect to node")
		srv.logs.Yellow("node failed to reconnect")
		return err
	}

	return nil
}

func (srv *discoveryAgent) connectToServerAddr(addr string) error {
	logctx := srv.logs.WithTitle("discoveryAgent.connectToServerAddr").With("server-addr", addr)

	uri, err := url.Parse(addr)
	if err != nil {
		logctx.Error(err, "failed to parse server address")
		return err
	}

	logctx = logctx.With("server-scheme", uri.Scheme).With("server-host", uri.Host)

	var c mnet.Client

	switch uri.Scheme {
	case "tcp":
		c, err = mtcp.Connect(uri.Host, mtcp.TLSConfig(srv.intent.TLS))
	case "ws":
		c, err = msocks.Connect(uri.Host, msocks.TLSConfig(srv.intent.TLS))
	default:
		logctx.Error(ErrUnknownScheme, "received address of unknown scheme")
		return ErrUnknownScheme
	}

	if err != nil {
		logctx.Error(err, "failed to connect to discovery service server")
		return err
	}

	if !srv.initd {
		srv.initd = true
		srv.observers = append(srv.observers, ObservationCenter{
			Addr:     uri.Host,
			Protocol: uri.Scheme,
		})
	}

	var meta ServiceMeta
	meta.Meta = srv.intent.Meta
	meta.Protocol = uri.Scheme
	meta.Secret = srv.intent.Secret
	meta.Region = srv.intent.Region
	meta.Service = srv.intent.Service
	meta.Addr = srv.intent.ServiceAddr
	meta.Interests = srv.intent.Interests

	srv.logs.With("meta", meta).With("id", c.ID).With("protocol", uri.Scheme)

	srv.cl.Lock()
	srv.c = &c
	srv.srv = meta
	srv.protocol = uri.Scheme
	srv.cl.Unlock()

	logctx.Info("connection ready")

	return nil
}

func (srv *discoveryAgent) handshake() error {
	logCtx := srv.logs.WithTitle("discoveryAgent.handshake")

	logCtx.Info("handshake step: send agent service meta information")
	if err := srv.deliverMeta(); err != nil {
		logCtx.Error(err, "failed to deliver meta info")
		return err
	}

	logCtx.Info("handshake step: read handshake complete signal")
	if err := srv.readHandshakeComplete(); err != nil {
		logCtx.Error(err, "failed to read handshake complete signal")
		return err
	}

	logCtx.Info("handshake step: read server service stats")
	if err := srv.readStats(); err != nil {
		logCtx.Error(err, "failed to read read initial server stats")
		return err
	}

	logCtx.Info("handshake completed")
	return nil
}

func (srv *discoveryAgent) readUntilClose() {
	defer atomic.StoreInt64(&srv.disconneted, 1)

	logCtx := srv.logs.WithTitle("discoveryAgent.readUntilClose")

	for {
		select {
		case <-srv.stats.C:
			srv.stats.Reset(statsInterval)
			if err := srv.requestStats(); err != nil {
				logCtx.Error(err, "failed to request server stats")
				return
			}

		default:
			msg, err := srv.c.Read()
			if err != nil {
				if err != mnet.ErrNoDataYet {
					return
				}
				continue
			}

			if bytes.HasPrefix(msg, pings) {
				logCtx.Info("discovery agent received received ping checkup")
				if err := srv.sendPongs(); err != nil {
					logCtx.Error(err, "discovery agent failed to send pong response")
					return
				}
				continue
			}

			if bytes.HasPrefix(msg, recordStatRes) {
				logCtx.Info("discovery agent received received sever stats")
				msg = bytes.TrimPrefix(msg, recordStatRes)
				if err := srv.updateStats(msg); err != nil {
					logCtx.Error(err, "discovery agent failed to process server stats")
					return
				}
				continue
			}

			if bytes.Equal(msg, healthReq) && srv.intent.Type == ServiceNode {
				logCtx.Info("discovery agent received health checkup request")
				srv.healtChan <- struct{}{}
				logCtx.Info("discovery agent sent health checkup through channel")
			}
		}
	}
}

func (srv *discoveryAgent) readUntil(ts time.Duration) ([]byte, error) {
	maxTime := time.Now().Add(ts)
	for {
		msg, err := srv.c.Read()
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

func (srv *discoveryAgent) readTilMessage() ([]byte, error) {
	for {
		msg, err := srv.c.Read()
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

func (srv *discoveryAgent) updateStats(msg []byte) error {
	var stats DiscoveryReport
	if err := json.Unmarshal(msg, &stats); err != nil {
		return err
	}

	if len(stats.ServerNodes) != 0 {
		srv.obl.Lock()
		for _, ob := range stats.ServerNodes {
			if _, ok := srv.obKnown[ob.Addr]; !ok {
				srv.obKnown[ob.Addr] = struct{}{}
				srv.observers = append(srv.observers, ob)
			}
		}
		srv.obl.Unlock()
	}

	srv.intent.Fn(stats.Reports)
	return nil
}

func (srv *discoveryAgent) deliverMeta() error {
	logCtx := srv.logs.WithTitle("discoveryAgent.deliverMeta")

	var header []byte

	switch srv.intent.Type {
	case ServiceNode:
		logCtx.Info("initializing agent has as Service")
		header = srvHandshakeHeader
	case ObservingNode:
		logCtx.Info("initializing agent has as Observer")
		header = obsHandshakeHeader
	}

	metaJSON, err := json.Marshal(srv.srv)
	if err != nil {
		logCtx.Error(err, "failed to marshal agent meta into json")
		return err
	}

	writer, err := srv.c.Write(len(metaJSON) + len(header))
	if err != nil {
		logCtx.Error(err, "failed to acquire writer for data")
		return err
	}

	hi, err := writer.Write(header)
	if err != nil {
		logCtx.Error(err, "failed to write header")
		return err
	}

	if hi != len(header) {
		logCtx.Error(err, "failed to fully write header")
		return io.ErrShortWrite
	}

	ni, err := writer.Write(metaJSON)
	if err != nil {
		logCtx.Error(err, "failed to write body")
		return err
	}

	if ni != len(metaJSON) {
		logCtx.Error(err, "failed to fully write body")
		return io.ErrShortWrite
	}

	if err = writer.Close(); err != nil {
		logCtx.Error(err, "failed to fully flush header and body data")
		return err
	}

	logCtx.Info("flushing all data written to connection")
	return srv.c.Flush()
}

func (srv *discoveryAgent) requestStats() error {
	writer, err := srv.c.Write(len(recordStats))
	if err != nil {
		return err
	}

	hi, err := writer.Write(recordStats)
	if err != nil {
		return err
	}

	if hi != len(recordStats) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return srv.c.Flush()
}

func (srv *discoveryAgent) sendPongs() error {
	writer, err := srv.c.Write(len(pongs))
	if err != nil {
		return err
	}

	n, err := writer.Write(pongs)
	if err != nil {
		return err
	}

	if n != len(pongs) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return srv.c.Flush()
}

func (srv *discoveryAgent) requestNodes() error {
	writer, err := srv.c.Write(len(clusterStats))
	if err != nil {
		return err
	}

	hi, err := writer.Write(clusterStats)
	if err != nil {
		return err
	}

	if hi != len(clusterStats) {
		return io.ErrShortWrite
	}

	err = writer.Close()
	if err != nil {
		return err
	}

	return srv.c.Flush()
}

func (srv *discoveryAgent) readStats() error {
	msg, err := srv.readUntil(statusReadWait)
	if err != nil {
		return err
	}

	if !bytes.HasPrefix(msg, recordStatRes) {
		return ErrNoNodeResponseRecv
	}

	msg = bytes.TrimPrefix(msg, recordStatRes)
	return srv.updateStats(msg)
}

func (srv *discoveryAgent) readNodes() error {
	msg, err := srv.readUntil(nodesReadWait)
	if err != nil {
		return err
	}

	if !bytes.HasPrefix(msg, clusterStatRes) {
		return ErrNoNodeResponseRecv
	}

	msg = bytes.TrimPrefix(msg, clusterStatRes)
	var nodes []ObservationCenter
	if err := json.Unmarshal(msg, &nodes); err != nil {
		return err
	}

	srv.obl.Lock()
	srv.observers = append(srv.observers, nodes...)
	srv.obl.Unlock()
	return nil
}

func (srv *discoveryAgent) readHandshakeComplete() error {
	msg, err := srv.readUntil(metaWait)
	if err != nil {
		return err
	}

	if !bytes.Equal(msg, handshakeDone) {
		return ErrNoHandshakeCompletion
	}

	return nil
}
