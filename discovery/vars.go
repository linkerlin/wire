package discovery

import (
	"errors"
	"time"
)

// errors ...
var (
	ErrNoServerAddr      = errors.New("Intent.ServerAddr can not be empty; must point to a discovery server")
	ErrNoServiceAddr     = errors.New("Intent.ServiceAddr can not be empty")
	ErrNoRegion          = errors.New("Intent.Region can not be empty")
	ErrNoService         = errors.New("Intent.Service can not be empty")
	ErrNoFn              = errors.New("Intent.Fn is required")
	ErrReadTimeout       = errors.New("read timeout")
	ErrUnknownScheme     = errors.New("unknown scheme")
	ErrInvalidProtocol   = errors.New("invalid protocol")
	ErrUnknownNodeType   = errors.New("unkown node type")
	ErrNoDiscoveryServer = errors.New("discovery server not started")
)

// constants for Node.
const (
	maxPingPongDiff = 10
	metaWait        = time.Second * 5
	pingInterval    = time.Second * 5
	statsInterval   = time.Second * 5
	inBetweenReads  = time.Millisecond * 100
	maxLastLiveness = time.Minute * 30
)

var (
	obsHandshakeHeader = []byte("OBS ")
	srvHandshakeHeader = []byte("SRV ")
	handshakeRescue    = []byte("-HKS ")
	handshakeDone      = []byte("+HKS ")
	pings              = []byte("DPING")
	pongs              = []byte("DPONG")
	srvLive            = []byte("SRVLIVE")
	recordStats        = []byte("STATS")
	recordStatRes      = []byte("RSTATS ")
	clusterStats       = []byte("CLSTTS")
	clusterStatRes     = []byte("RLSTTS ")
	healthReq          = []byte("HLT-")
	healthRes          = []byte("HLT+ ")
)
