package discovery

import "github.com/influx6/mnet"

// Service embodies data related to a giving service within a giving region.
type Service struct {
	Info   mnet.Info
	Region string
}

type ServiceMap map[string][]Service

// DiscoveryService implements a discovery server which provides a simple registery
// of available services all marked with giving tags or pre-selected values.
type DiscoveryService struct {
	services ServiceMap
}
