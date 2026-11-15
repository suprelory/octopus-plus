package client

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
)

func TestSOCKSDialCancellationClosesHandshake(t *testing.T) {
	for _, scheme := range []string{"socks", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			testSOCKSDialCancellationClosesHandshake(t, scheme)
		})
	}
}

func testSOCKSDialCancellationClosesHandshake(t *testing.T, scheme string) {
	t.Helper()
	for _, stage := range []string{"greeting", "authentication", "connect"} {
		t.Run(stage, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ready := make(chan error, 1)
			closed := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					ready <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				header := make([]byte, 2)
				if _, err = io.ReadFull(conn, header); err == nil {
					_, err = io.CopyN(io.Discard, conn, int64(header[1]))
				}
				if err == nil && stage != "greeting" {
					method := byte(0)
					if stage == "authentication" {
						method = 2
					}
					_, err = conn.Write([]byte{5, method})
					if err == nil && stage == "authentication" {
						_, err = io.ReadFull(conn, header)
						if err == nil {
							_, err = io.CopyN(io.Discard, conn, int64(header[1]))
						}
						if err == nil {
							_, err = io.ReadFull(conn, header[:1])
						}
						if err == nil {
							_, err = io.CopyN(io.Discard, conn, int64(header[0]))
						}
					} else if err == nil {
						// CONNECT to the fixed IPv4 destination below.
						_, err = io.CopyN(io.Discard, conn, 10)
					}
				}
				ready <- err
				_, err = conn.Read(header)
				closed <- err
			}()
			client, err := newHTTPClientCustomProxy(scheme + "://user:pass@" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				conn, err := client.Transport.(*http.Transport).DialContext(ctx, "tcp", "127.0.0.1:80")
				if conn != nil {
					conn.Close()
				}
				done <- err
			}()
			select {
			case err := <-ready:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handshake not reached")
			}
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled dial succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("dial ignored cancellation")
			}
			select {
			case err := <-closed:
				if err != io.EOF {
					t.Fatalf("connection not closed: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("proxy connection leaked")
			}
		})
	}
}

func TestSOCKSProxyRequests(t *testing.T) {
	resetProxyClientCache()
	t.Cleanup(resetProxyClientCache)
	for _, mode := range []string{"request", "probe"} {
		for _, scheme := range []string{"socks", "socks5", "socks5h"} {
			for _, authenticate := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/auth=%t", mode, scheme, authenticate), func(t *testing.T) {
					// A reserved .invalid domain must reach the proxy without local DNS.
					host := "upstream.invalid"
					if mode == "probe" {
						// The probe API checks that its target is public before dialing.
						host = "1.1.1.1"
					}
					listener, err := net.Listen("tcp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					defer listener.Close()
					result := make(chan error, 1)
					go func() {
						conn, err := listener.Accept()
						if err != nil {
							result <- err
							return
						}
						defer conn.Close()
						_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
						result <- serveSOCKSHTTP(conn, host, authenticate)
					}()
					proxyURL := scheme + "://"
					if authenticate {
						proxyURL += "alice:socks-secret@"
					}
					proxyURL += listener.Addr().String()
					target := "http://" + host + "/verify"
					if mode == "probe" {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						got, err := op.ProxyConfigurationTest(model.ProxyTestRequest{ProxyURL: proxyURL, URL: target}, ctx)
						if err != nil || !got.Success || got.StatusCode != http.StatusOK {
							t.Fatalf("proxy test = %+v, %v", got, err)
						}
					} else {
						client, err := GetHTTPClientCustomProxy(proxyURL)
						if err != nil {
							t.Fatal(err)
						}
						defer client.CloseIdleConnections()
						client.Timeout = 5 * time.Second
						response, err := client.Get(target)
						if err != nil {
							t.Fatal(err)
						}
						body, err := io.ReadAll(response.Body)
						response.Body.Close()
						if err != nil || response.StatusCode != http.StatusOK || string(body) != "socks" {
							t.Fatalf("SOCKS response: status=%d body=%q err=%v", response.StatusCode, body, err)
						}
					}
					if err := <-result; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func serveSOCKSHTTP(conn net.Conn, host string, authenticate bool) error {
	r := bufio.NewReader(conn)
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return err
	}
	if header[0] != 5 {
		return fmt.Errorf("unexpected SOCKS version: %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(r, methods); err != nil {
		return err
	}
	method := byte(0)
	if authenticate {
		method = 2
	}
	if !bytes.Contains(methods, []byte{method}) {
		return fmt.Errorf("SOCKS authentication method %d not offered", method)
	}
	if _, err := conn.Write([]byte{5, method}); err != nil {
		return err
	}
	if authenticate {
		wantAuth := append([]byte{1, 5}, []byte("alice")...)
		wantAuth = append(wantAuth, byte(len("socks-secret")))
		wantAuth = append(wantAuth, []byte("socks-secret")...)
		if err := expectSOCKSFrame(r, wantAuth); err != nil {
			return fmt.Errorf("SOCKS credentials: %w", err)
		}
		if _, err := conn.Write([]byte{1, 0}); err != nil {
			return err
		}
	}
	wantConnect := []byte{5, 1, 0}
	if ip := net.ParseIP(host).To4(); ip != nil {
		wantConnect = append(wantConnect, 1)
		wantConnect = append(wantConnect, ip...)
	} else {
		wantConnect = append(wantConnect, 3, byte(len(host)))
		wantConnect = append(wantConnect, []byte(host)...)
	}
	wantConnect = append(wantConnect, 0, 80)
	if err := expectSOCKSFrame(r, wantConnect); err != nil {
		return fmt.Errorf("SOCKS target: %w", err)
	}
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80}); err != nil {
		return err
	}
	request, err := http.ReadRequest(r)
	if err != nil {
		return err
	}
	request.Body.Close()
	if request.Method != http.MethodGet || request.Host != host || request.URL.Path != "/verify" {
		return fmt.Errorf("unexpected HTTP request: %s %s %s", request.Method, request.Host, request.URL)
	}
	_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nsocks")
	return err
}

func expectSOCKSFrame(r io.Reader, want []byte) error {
	got := make([]byte, len(want))
	if _, err := io.ReadFull(r, got); err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("frame = %x, want %x", got, want)
	}
	return nil
}
