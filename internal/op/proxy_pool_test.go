package op

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
)

func TestProxyConfigurationSOCKS5H(t *testing.T) {
	ctx := setupSiteOpTestDB(t)
	proxyConfigurationCache.Clear()
	t.Cleanup(proxyConfigurationCache.Clear)
	item := &model.ProxyConfiguration{
		Name: "remote-dns", URL: " SOCKS5H://Alice:p%40ss@Proxy.Example:1080 ", Enabled: true,
	}
	if err := ProxyConfigurationCreate(item, ctx); err != nil {
		t.Fatal(err)
	}
	checkURL := func(want string) {
		t.Helper()
		stored, err := ProxyConfigurationGet(item.ID, ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stored.URL != want {
			t.Fatalf("stored proxy URL = %q, want %q", stored.URL, want)
		}
		got, err := ProxyURLForConfig(item.ID, ctx)
		if err != nil || got != want {
			t.Fatalf("resolved proxy URL = %q, %v; want %q", got, err, want)
		}
	}
	checkURL("socks5h://Alice:p%40ss@proxy.example:1080")
	updatedURL := "socks5h://Bob:new-pass@[::1]:1081"
	if _, err := ProxyConfigurationUpdate(&model.ProxyConfigurationUpdateRequest{ID: item.ID, URL: &updatedURL}, ctx); err != nil {
		t.Fatal(err)
	}
	checkURL(updatedURL)
	proxyConfigurationCache.Clear()
	checkURL(updatedURL)
}

func TestProxyTestSOCKS5HDialCancellation(t *testing.T) {
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
		// Read the unauthenticated greeting, then stall the handshake.
		_, err = io.ReadFull(conn, make([]byte, 3))
		ready <- err
		_, err = io.Copy(io.Discard, conn)
		closed <- err
	}()
	client, err := newProxyTestHTTPClient("socks5h://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		conn, err := client.Transport.(*http.Transport).DialContext(ctx, "tcp", "upstream.invalid:80")
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
		t.Fatal("SOCKS handshake not reached")
	}
	cancel()
	select {
	case err := <-done:
		// x/net interrupts a SOCKS handshake by expiring the socket deadline,
		// so cancellation can surface as an I/O timeout.
		if err == nil {
			t.Fatal("cancelled SOCKS probe dial succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS probe dial ignored cancellation")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("proxy connection not closed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SOCKS probe connection leaked")
	}
}
