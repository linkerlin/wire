package msocks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gokit/history"
	"github.com/gokit/history/handlers/discard"
	"github.com/influx6/faux/tests"
	"github.com/wirekit/wire"
	"github.com/wirekit/wire/msocks"
)

var (
	_      = history.SetDefaultHandlers(discard.Discard)
	dialer = &net.Dialer{Timeout: 2 * time.Second}
)

func TestWebsocketServerWithMWebsocketClient(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	netw, err := createNewNetwork(ctx, "localhost:4050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully created network")
	}
	tests.Passed("Should have successfully created network")

	client, err := msocks.Connect("ws://localhost:4050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully connected to network")
	}
	tests.Passed("Should have successfully connected to network")

	payload := []byte("pub help")
	cw, err := client.Write(len(payload))
	if err != nil {
		tests.FailedWithError(err, "Should have successfully created new writer")
	}
	tests.Passed("Should have successfully created new writer")

	cw.Write(payload)
	if err := cw.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully written payload to client")
	}
	tests.Passed("Should have successfully written payload to client")

	if ferr := client.Flush(); ferr != nil {
		tests.FailedWithError(ferr, "Should have successfully flush data to network")
	}
	tests.Passed("Should have successfully flush data to network")

	var res []byte
	var readErr error
	for {
		res, readErr = client.Read()
		if readErr != nil && readErr == wire.ErrNoDataYet {
			continue
		}

		break
	}

	if readErr != nil {
		tests.FailedWithError(readErr, "Should have successfully read reply from network")
	}
	tests.Passed("Should have successfully read reply from network")

	expected := []byte("now publishing to [help]\r\n")
	if !bytes.Equal(res, expected) {
		tests.Info("Received: %+q", res)
		tests.Info("Expected: %+q", expected)
		tests.FailedWithError(err, "Should have successfully matched expected data with received from network")
	}
	tests.Passed("Should have successfully matched expected data with received from network")

	if cerr := client.Close(); cerr != nil {
		tests.FailedWithError(cerr, "Should have successfully closed client connection")
	}
	tests.Passed("Should have successfully closed client connection")

	cancel()
	netw.Wait()
}

func TestNetwork_Add(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	netw, err := createNewNetwork(ctx, "localhost:4050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	netw2, err := createInfoNetwork(ctx, "localhost:7050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully created network")
	}
	tests.Passed("Should have successfully created network")

	// Send net.Conn to be manage by another network manager for talks.
	if err := netw2.AddCluster("localhost:4050"); err != nil {
		tests.FailedWithError(err, "Should have successfully added net.Conn to network")
	}
	tests.Passed("Should have successfully added net.Conn to network")

	client, err := msocks.Connect("localhost:7050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully connected to network")
	}
	tests.Passed("Should have successfully connected to network")

	payload := []byte("pub")
	cw, err := client.Write(len(payload))
	if err != nil {
		tests.FailedWithError(err, "Should have successfully created new writer")
	}
	tests.Passed("Should have successfully created new writer")

	cw.Write(payload)
	if err := cw.Close(); err != nil {
		tests.FailedWithError(err, "Should have successfully written payload to client")
	}
	tests.Passed("Should have successfully written payload to client")

	if ferr := client.Flush(); ferr != nil {
		tests.FailedWithError(ferr, "Should have successfully flush data to network")
	}
	tests.Passed("Should have successfully flush data to network")

	var msg []byte
	for {
		msg, err = client.Read()
		if err != nil {
			if err == wire.ErrNoDataYet {
				err = nil
				continue
			}

		}
		break
	}

	client.Close()

	var infos []wire.Info
	if err := json.Unmarshal(msg, &infos); err != nil {
		tests.FailedWithError(err, "Should have successfully decoded response")
	}
	tests.Passed("Should have successfully decoded response")

	cancel()
	netw.Wait()
	netw2.Wait()

	if len(infos) != 2 {
		tests.Failed("Should have received 2 info other to clients")
	}
	tests.Passed("Should have received 2 info other to clients")

	cluster := infos[1]
	if cluster.ServerAddr != "127.0.0.1:4050" {
		tests.Failed("Should have matched cluster server to second mnet network")
	}
	tests.Passed("Should have matched cluster server to second mnet network")

}

func TestNetwork_ClusterConnect(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())

	netw, err := createNewNetwork(ctx, "localhost:4050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	netw2, err := createInfoNetwork(ctx, "localhost:7050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	// Should succesfully connect to cluster on localhost:4050
	if err := netw2.AddCluster("localhost:4050"); err != nil {
		tests.FailedWithError(err, "Should have successfully connect to cluster")
	}
	tests.Passed("Should have successfully connect to cluster")

	// Should fail to connect to cluster on localhost:7050 since we are connected already.
	if err := netw.AddCluster("localhost:7050"); err == nil {
		tests.Failed("Should have failed to connect to already connected cluster")
	}
	tests.Passed("Should have failed to connect to already connected cluster")

	cancel()
	netw.Wait()
	netw2.Wait()
}

func createNewNetwork(ctx context.Context, addr string) (*msocks.WebsocketNetwork, error) {
	var netw msocks.WebsocketNetwork
	netw.Addr = addr

	netw.Handler = func(client wire.Client) error {
		for {
			message, err := client.Read()
			if err != nil {
				if err == wire.ErrNoDataYet {
					time.Sleep(300 * time.Millisecond)
					continue
				}

				return err
			}

			messages := strings.Split(string(message), " ")
			if len(messages) == 0 {
				continue
			}

			command := messages[0]
			rest := messages[1:]
			tests.Info("Websocket Server received: %q -> %+q", command, rest)

			switch command {
			case "pub":
				res := []byte(fmt.Sprintf("now publishing to %+s\r\n", rest))
				w, err := client.Write(len(res))
				if err != nil {
					return err
				}

				w.Write(res)
				w.Close()
			case "sub":
				res := []byte(fmt.Sprintf("subscribed to %+s\r\n", rest))
				w, err := client.Write(len(res))
				if err != nil {
					return err
				}

				w.Write(res)
				w.Close()
			}

			if err := client.Flush(); err != nil {
				if err == io.ErrShortWrite {
					continue
				}

				return err
			}
		}
	}

	return &netw, netw.Start(ctx)
}

func createInfoNetwork(ctx context.Context, addr string) (*msocks.WebsocketNetwork, error) {
	var netw msocks.WebsocketNetwork
	netw.Addr = addr

	netw.Handler = func(client wire.Client) error {
		for {
			_, err := client.Read()
			if err != nil {
				if err == wire.ErrNoDataYet {
					time.Sleep(300 * time.Millisecond)
					continue
				}

				return err
			}

			var infos []wire.Info
			infos = append(infos, client.Info())
			if others, err := client.Others(); err == nil {
				for _, item := range others {
					infos = append(infos, item.Info())
				}

				jsn, err := json.Marshal(infos)
				if err != nil {
					return err
				}

				wc, err := client.Write(len(jsn))
				if err != nil {
					return err
				}

				wc.Write(jsn)
				wc.Close()
			}

			if err := client.Flush(); err != nil {
				if err == io.ErrShortWrite {
					continue
				}

				return err
			}
		}
	}

	return &netw, netw.Start(ctx)
}
