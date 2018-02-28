package discovery_test

import (
	"context"
	"os"
	"testing"

	"sync"

	"github.com/influx6/faux/metrics"
	"github.com/influx6/faux/metrics/custom"
	"github.com/influx6/faux/netutils"
	"github.com/influx6/faux/tests"
	"github.com/influx6/mnet/discovery"
	"github.com/influx6/mnet/msocks"
	"github.com/influx6/mnet/mtcp"
)

var (
	logs = metrics.New()
)

func TestClusteredDiscoveryServer(t *testing.T) {
	if testing.Verbose() {
		logs = metrics.New(custom.StackDisplay(os.Stderr))
	}

	ctx, cancel := context.WithCancel(context.Background())

	addr := netutils.ResolveAddr("tcp://localhost:0")
	addr2 := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Metrics = logs
	tx.Addr = addr

	var tx2 discovery.Service
	tx2.Metrics = logs
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
	if testing.Verbose() {
		logs = metrics.New(custom.StackDisplay(os.Stderr))
	}

	var wg sync.WaitGroup
	wg.Add(1)

	addr := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Metrics = logs
	tx.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	service, err := discovery.Agent(logs, discovery.Intent{
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
	if testing.Verbose() {
		logs = metrics.New(custom.StackDisplay(os.Stderr))
	}

	var wg sync.WaitGroup
	wg.Add(1)

	addr := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Metrics = logs
	tx.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	service, err := discovery.Agent(logs, discovery.Intent{
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
	if testing.Verbose() {
		logs = metrics.New(custom.StackDisplay(os.Stderr))
	}

	var serviceRes, obRes []discovery.ServiceReport

	var wg sync.WaitGroup
	wg.Add(2)

	addr := netutils.ResolveAddr("tcp://localhost:0")

	var tx discovery.Service
	tx.Metrics = logs
	tx.Addr = addr

	ctx, cancel := context.WithCancel(context.Background())
	if err := tx.Start(ctx); err != nil {
		tests.FailedWithError(err, "Should have successfully started discovery server")
	}
	tests.Passed("Should have successfully started discovery server")

	service, err := discovery.Agent(logs, discovery.Intent{
		ServerAddr:  addr,
		Service:     "surga",
		Secret:      "sygar-slicker",
		Region:      "africa-west",
		Type:        discovery.ServiceNode,
		ServiceAddr: netutils.ResolveAddr("tcp://190.23.232.12:4050"),
		Fn: func(reports []discovery.ServiceReport) {
			serviceRes = reports
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

	ob, err := discovery.Agent(logs, discovery.Intent{
		ServerAddr: addr,
		Region:     "africa-west",
		Type:       discovery.ObservingNode,
		Fn: func(reports []discovery.ServiceReport) {
			obRes = reports
			wg.Done()
		},
	})
	if err != nil {
		tests.FailedWithError(err, "Should have successfully started observer client")
	}
	tests.Passed("Should have successfully started observer client")

	go drainChan(ob.Disconnects())
	go drainChan(ob.CloseNotifier())
	go drainChan(ob.Health())

	wg.Wait()
	_ = cancel
	//ob.Stop()
	//service.Stop()
	//cancel()
	tx.Wait()

	if len(serviceRes) != 0 {
		tests.Info("Expected: %d", 0)
		tests.Info("Received: %d", len(serviceRes))
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
	if testing.Verbose() {
		logs = metrics.New(custom.StackDisplay(os.Stderr))
	}

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

				client.Close()
				return nil
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

				client.Close()
				return nil
			},
		},
	}

	for _, spec := range specs {
		var tx discovery.Service
		tx.Metrics = logs

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
	}
}

func drainChan(c chan struct{}) {
	for range c {
	}
}
