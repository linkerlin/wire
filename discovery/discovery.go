package discovery

import "github.com/influx6/mnet"

// Service implements a structure which contains cluster data information
// from a given service.
type Service struct {
	ID       string
	Region   string
	Info     mnet.Info
	Addr     string
	Protocol string
}

// Node embodies data related to a giving service within a giving region.
type Node struct {
	Service      Service
	Liveness     int64
	MissedPings  int64
	RespondPings int64
	client       mnet.Client
}

// DiscoveryService implements a discovery server which provides a simple registery
// of available services all marked with giving tags or pre-selected values.
type DiscoveryService struct {
	services map[string]Service
}
