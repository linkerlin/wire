package mtcp_test

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"context"

	"github.com/gokit/history"
	"github.com/gokit/history/handlers/discard"
	"github.com/influx6/faux/tests"
	"github.com/wirekit/tlsfs/certificates"
	"github.com/wirekit/wire"
	"github.com/wirekit/wire/mtcp"
)

var (
	_      = history.SetDefaultHandlers(discard.Discard)
	dialer = &net.Dialer{Timeout: 2 * time.Second}
)

func TestNetwork_Add(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	netw, err := createNewNetwork(ctx, "localhost:4050", nil)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	netw2, err := createInfoNetwork(ctx, "localhost:7050", nil)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	// Send net.Conn to be manage by another network manager for talks.
	if err := netw2.AddCluster("localhost:4050"); err != nil {
		tests.FailedWithError(err, "Should have successfully added net.Conn to network")
	}
	tests.Passed("Should have successfully added net.Conn to network")

	client, err := mtcp.Connect("localhost:7050")
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

	var infos []wire.Info
	if err := json.Unmarshal(msg, &infos); err != nil {
		tests.Info("Received: %+q\n", msg)
		tests.FailedWithError(err, "Should have successfully decoded response")
	}
	tests.Passed("Should have successfully decoded response")

	client.Close()
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

func TestNetwork_Cluster_Broadcast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	netw, err := createNewNetwork(ctx, "localhost:4050", nil)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	netw2, err := createNewNetwork(ctx, "localhost:7050", nil)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully created network")
	}
	tests.Passed("Should have successfully created network")

	// Send net.Conn to be manage by another network manager for talks.
	if err := netw2.AddCluster("localhost:4050"); err != nil {
		tests.FailedWithError(err, "Should have successfully added net.Conn to network")
	}
	tests.Passed("Should have successfully added net.Conn to network")

	client2, err := mtcp.Connect("localhost:4050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully connected to network")
	}
	tests.Passed("Should have successfully connected to network")

	client, err := mtcp.Connect("localhost:7050")
	if err != nil {
		tests.FailedWithError(err, "Should have successfully connected to network")
	}
	tests.Passed("Should have successfully connected to network")

	payload := []byte("pub")
	cw, err := client.Broadcast(len(payload))
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
		msg, err = client2.Read()
		if err != nil {
			if err == wire.ErrNoDataYet {
				err = nil
				continue
			}

		}
		break
	}

	client.Close()
	client2.Close()

	if !bytes.Equal(msg, payload) {
		tests.Failed("Should have received broadcasted message")
	}
	tests.Passed("Should have received broadcasted message")

	cancel()
	netw.Wait()
	netw2.Wait()
}

func TestNetwork_ClusterConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	netw, err := createNewNetwork(ctx, "localhost:4050", nil)
	if err != nil {
		tests.FailedWithError(err, "Should have successfully create network")
	}
	tests.Passed("Should have successfully create network")

	netw2, err := createInfoNetwork(ctx, "localhost:7050", nil)
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

func readMessage(conn net.Conn) ([]byte, error) {
	incoming := make([]byte, 4)
	n, err := conn.Read(incoming)
	if err != nil {
		return nil, err
	}

	expectedLen := binary.BigEndian.Uint32(incoming[:n])
	data := make([]byte, expectedLen)
	n, err = conn.Read(data)
	if err != nil {
		return nil, err
	}

	if n != int(expectedLen) {
		return data, errors.New("expected length unmarched")
	}

	return data[:n], nil
}

func makeMessage(msg []byte) []byte {
	header := make([]byte, 4, len(msg)+4)
	binary.BigEndian.PutUint32(header, uint32(len(msg)))
	header = append(header, msg...)
	return header
}

func createTLSCA() (ca certificates.CertificateAuthority, server, client certificates.CertificateRequest, err error) {
	service := certificates.CertificateAuthorityProfile{
		CommonName:   "*",
		Local:        "Lagos",
		Organization: "DreamBench",
		Country:      "Nigeria",
		Province:     "South-West",
	}

	service.KeyStrength = 4096
	service.LifeTime = (time.Hour * 8760)
	service.Emails = append([]string{}, "alex.ewetumo@dreambench.io")

	var requestService certificates.CertificateRequestProfile
	requestService.KeyStrength = 2048
	requestService.CommonName = "*"

	ca, err = certificates.CreateCertificateAuthority(service)
	if err != nil {
		return
	}

	if server, err = certificates.CreateCertificateRequest(requestService); err == nil {
		if err = ca.ApproveServerCertificateSigningRequest(&server, time.Hour*8760); err != nil {
			return
		}
	}

	if client, err = certificates.CreateCertificateRequest(requestService); err == nil {
		if err = ca.ApproveClientCertificateSigningRequest(&client, time.Hour*8760); err != nil {
			return
		}
	}

	return
}

func createNewNetwork(ctx context.Context, addr string, config *tls.Config) (*mtcp.TCPNetwork, error) {
	var netw mtcp.TCPNetwork
	netw.Addr = addr
	netw.TLS = config

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

func createInfoNetwork(ctx context.Context, addr string, config *tls.Config) (*mtcp.TCPNetwork, error) {
	var netw mtcp.TCPNetwork
	netw.Addr = addr
	netw.TLS = config

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
