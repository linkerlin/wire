package discovery_test

import (
	"context"
	"fmt"
	"testing"

	"sync"

	"github.com/gokit/history"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/faux/tests"
	"github.com/influx6/mnet/discovery"
	"github.com/influx6/mnet/msocks"
	"github.com/influx6/mnet/mtcp"

	"github.com/gokit/history/handlers/discard"
)

var (
	_ = history.SetDefaultHandlers(discard.Discard)
)

func TestClusteredDiscoveryServer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	addr := netutils.ResolveAddr("tcp://localhost:0")
	addr2 := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Addr = addr

	var tx2 discovery.Service
	tx2.Addr = addr2

	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	if err := tx2.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	if err := tx.AddCluster(addr2); err != nil {
		tests.FailedWithError(err, "Should have successfully connected to discovery cluster")
	}
	tests.Passed("Should have successfully connected to discovery cluster")

	cancel()
	tx.Wait()
	tx2.Wait()
}

func TestAgentNode_Observer(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	addr := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	service, err := discovery.Agent(discovery.Intent{
		Type:       discovery.ObservingNode,
		ServerAddr: addr,
		Region:     "africa-west",
		Fn: func(reports []discovery.ServiceReport) {
			wg.Done()
		},
	})
	if err != nil {
		tests.FailedWithError(err, "Should have successfully started service client")
	}
	tests.Passed("Should have successfully started service client")

	go drainChan(service.Disconnects())
	go drainChan(service.CloseNotifier())
	go drainChan(service.Health())

	wg.Wait()
	service.Stop()
	cancel()
	tx.Wait()
}

func TestAgentNode_Service(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)

	addr := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	service, err := discovery.Agent(discovery.Intent{
		Type:        discovery.ServiceNode,
		ServerAddr:  addr,
		Service:     "surga",
		Secret:      "sygar-slicker",
		Region:      "africa-west",
		ServiceAddr: netutils.ResolveAddr("tcp://190.23.232.12:4050"),
		Fn: func(reports []discovery.ServiceReport) {
			wg.Done()
		},
	})
	if err != nil {
		tests.FailedWithError(err, "Should have successfully started service client")
	}
	tests.Passed("Should have successfully started service client")

	go drainChan(service.Disconnects())
	go drainChan(service.CloseNotifier())
	go drainChan(service.Health())

	wg.Wait()
	service.Stop()
	cancel()
	tx.Wait()
}

func TestAgentNode_ServiceWithObserver(t *testing.T) {
	serviceReport := make(chan []discovery.ServiceReport, 1)
	observerReport := make(chan []discovery.ServiceReport, 1)

	var wg sync.WaitGroup
	wg.Add(3)

	addr := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	service, err := discovery.Agent(discovery.Intent{
		ServerAddr:  addr,
		Service:     "surga",
		Secret:      "sygar-slicker",
		Region:      "africa-west",
		Type:        discovery.ServiceNode,
		ServiceAddr: netutils.ResolveAddr("tcp://190.23.232.12:4050"),
		Fn: func(reports []discovery.ServiceReport) {
			if cap(serviceReport) != len(serviceReport) {
				serviceReport <- reports
				fmt.Printf("Received service Report: %#q\n", reports)
			}
		},
	})
	if err != nil {
		tests.FailedWithError(err, "Should have successfully started service client")
	}
	tests.Passed("Should have successfully started service client")

	go drainChan(service.Health())
	go drainChan(service.Disconnects())
	go drainChan(service.CloseNotifier())

	ob, err := discovery.Agent(discovery.Intent{
		ServerAddr: addr,
		Region:     "africa-west",
		Type:       discovery.ObservingNode,
		Fn: func(reports []discovery.ServiceReport) {
			if cap(observerReport) != len(observerReport) {
				observerReport <- reports
				fmt.Printf("Received observer Report: %#q\n", reports)
			}
		},
	})
	if err != nil {
		tests.FailedWithError(err, "Should have successfully started observer client")
	}
	tests.Passed("Should have successfully started observer client")

	go drainChan(ob.Disconnects())
	go drainChan(ob.CloseNotifier())
	go drainChan(ob.Health())

	srRes := <-serviceReport
	obRes := <-observerReport

	ob.Stop()
	service.Stop()
	cancel()
	tx.Wait()

	if len(srRes) != 0 {
		tests.Info("Expected: %d", 0)
		tests.Info("Received: %d", len(srRes))
		tests.Failed("Should have received zero registered services")
	}
	tests.Passed("Should have received zero registered services")

	if len(obRes) != 1 {
		tests.Info("Expected: %d", 1)
		tests.Info("Received: %d", len(obRes))
		tests.Failed("Should have received zero registered services")
	}
	tests.Passed("Should have received zero registered services")
}

func TestDiscoveryProtocols(t *testing.T) {
	specs := []struct {
		Title       string
		Addr        string
		TestConnect func() error
	}{
		{
			Title: "Start DiscoveryService with mtcp server",
			Addr:  "tcp://localhost:6060",
			TestConnect: func() error {
				client, err := mtcp.Connect("localhost:6060")
				if err != nil {
					return err
				}

				w, err := client.Write(10)
				if err != nil {
					return err
				}

				w.Write([]byte("DONE"))
				if err = w.Close(); err != nil {
					return err
				}

				if err = client.Flush(); err != nil {
					return err
				}

				return client.Close()
			},
		},
		{
			Title: "Start DiscoveryService with websocket server",
			Addr:  "ws://localhost:5060",
			TestConnect: func() error {
				client, err := msocks.Connect("localhost:5060")
				if err != nil {
					return err
				}

				w, err := client.Write(10)
				if err != nil {
					return err
				}

				w.Write([]byte("DONE"))
				if err = w.Close(); err != nil {
					return err
				}

				if err = client.Flush(); err != nil {
					return err
				}

				return client.Close()
			},
		},
	}

	for _, spec := range specs {
		var tx discovery.Service
		tests.Header(spec.Title)
		ctx, cancel := context.WithCancel(context.Background())

		tx.Addr = spec.Addr
		tx.Start(ctx)

		if err := spec.TestConnect(); err != nil {
			tests.ErroredWithError(err, "Should have successfully connected to network")
		}
		tests.Passed("Should have successfully connected to network")

		cancel()
		tx.Wait()
		tests.Passed("Should have successfully closed network")
	}
}

func drainChan(c chan struct{}) {
	for range c {
	}
}
