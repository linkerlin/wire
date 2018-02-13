package mnet

import "time"

const (
	// MinTemporarySleep sets the minimum, initial sleep a network should
	// take when facing a Temporary net error.
	MinTemporarySleep = 10 * time.Millisecond

	// MaxTemporarySleep sets the maximum, allowed sleep a network should
	// take when facing a Temporary net error.
	MaxTemporarySleep = 1 * time.Second

	// MaxFlushDeadline sets the maximum, allowed duration for flushing data
	MaxFlushDeadline = 3 * time.Second

	// MinBufferSize sets the initial size of space of the slice
	// used to read in content from a net.Conn in the connections
	// read loop.
	MinBufferSize = 512

	// MaxBufferSize sets the maximum size allowed for all reads
	// used in the readloop of a client's net.Conn.
	MaxBufferSize = 69560

	// DefaultDialTimeout sets the default maximum time in seconds allowed before
	// a net.Dialer exits attempt to dial a network.
	DefaultDialTimeout = 3 * time.Second

	// DefaultKeepAlive sets the default maximum time to keep alive a tcp connection
	// during no-use. It is used by net.Dialer.
	DefaultKeepAlive = 3 * time.Minute

	// MaxInfoWait sets the default duration to wait for arrival of connection info else
	// closing the connection.
	MaxInfoWait = time.Second * 5

	// MaxConnections sets a default maximum connection allowed for a giving network.
	MaxConnections = 8000

	// DefaultReconnectBufferSize sets the size of the buffer during reconnection.
	DefaultReconnectBufferSize = 1024 * 1024 * 8

	// HeaderLength defines the size of giving byte slice for message length header.
	HeaderLength = 4

	// MaxHeaderSize defines size of max header for message header length.
	MaxHeaderSize = uint32(4294967295)

	// CINFO defines a action key for requesting connection info.
	CINFO = "MNET:CINFO"

	// RINFO defines a action key for responding to a CINFO request.
	RINFO = "MNET:RINFO "

	// CLSTATUS defines a action key for responding with cluster status info.
	CLSTATUS = "MNET:CLSTATUS "

	// CLHandshake defines a action key for responding with a handshake completed.
	CLHANDSHAKECOMPLETED = "MNET:Handshake:Completed"
)
