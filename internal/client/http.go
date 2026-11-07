package client

import (
	"container/list"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/suprelory/octopus-plus/internal/model"
	"github.com/suprelory/octopus-plus/internal/op"
	"golang.org/x/net/proxy"
)

var (
	systemDirectClient *http.Client
	systemProxyClient  *http.Client
	systemProxyURL     string
	clientLock         sync.RWMutex
)

// maxProxyClients bounds the per-proxy client cache. Proxy configurations
// are admin-maintained, so a small bound comfortably holds every configured
// proxy while capping orphaned sockets if URLs churn.
const maxProxyClients = 32

type proxyClientCache struct {
	mu      sync.Mutex
	entries map[string]*list.Element // normalized proxy URL -> element
	order   *list.List               // LRU: front = most recent
}

type proxyClientEntry struct {
	key    string
	client *http.Client
}

var proxyClients = &proxyClientCache{
	entries: make(map[string]*list.Element),
	order:   list.New(),
}

// normalizeProxyURL canonicalizes the cache key so equivalent spellings
// share one client. Scheme and host are case-insensitive (unified here);
// credentials participate in the key verbatim — a changed proxy auth must
// yield a different client.
func normalizeProxyURL(proxyURLStr string) (string, error) {
	parsed, err := url.Parse(proxyURLStr)
	if err != nil {
		return "", fmt.Errorf("invalid proxy url: %w", err)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func (c *proxyClientCache) get(key string) *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.MoveToFront(element)
		return element.Value.(*proxyClientEntry).client
	}
	return nil
}

// put stores the client for key, evicting the least recently used entry
// (closing its idle connections) when the cache is full.
func (c *proxyClientCache) put(key string, client *http.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.order.MoveToFront(element)
		return
	}
	c.entries[key] = c.order.PushFront(&proxyClientEntry{key: key, client: client})
	for c.order.Len() > maxProxyClients {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		evicted := c.order.Remove(oldest).(*proxyClientEntry)
		delete(c.entries, evicted.key)
		if transport, ok := evicted.client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
}

// invalidate drops the client for key, closing its idle connections. Called
// when the proxy configuration behind the URL changes or is removed.
func (c *proxyClientCache) invalidate(key string) {
	c.mu.Lock()
	element, ok := c.entries[key]
	if ok {
		c.order.Remove(element)
		delete(c.entries, key)
	}
	c.mu.Unlock()
	if ok {
		if transport, ok := element.Value.(*proxyClientEntry).client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
}

// GetHTTPClientSystemProxy returns a cached http.Client.
// - useProxy=false: bypass proxy
// - useProxy=true: use proxy settings from system/app settings (setting key: proxy_url)
func GetHTTPClientSystemProxy(useProxy bool) (*http.Client, error) {
	if useProxy {
		currentProxyURL, err := op.SettingGetString(model.SettingKeyProxyURL)
		if err != nil {
			return nil, err
		}
		if currentProxyURL == "" {
			return nil, fmt.Errorf("proxy url is empty")
		}

		clientLock.RLock()
		if systemProxyClient != nil && systemProxyURL == currentProxyURL {
			clientLock.RUnlock()
			return systemProxyClient, nil
		}
		clientLock.RUnlock()

		clientLock.Lock()
		defer clientLock.Unlock()

		// Re-check after acquiring write lock.
		if systemProxyClient != nil && systemProxyURL == currentProxyURL {
			return systemProxyClient, nil
		}

		client, err := newHTTPClientCustomProxy(currentProxyURL)
		if err != nil {
			return nil, err
		}
		// Close the replaced client's idle connections so the old proxy's
		// sockets do not linger until IdleConnTimeout.
		if systemProxyClient != nil {
			if transport, ok := systemProxyClient.Transport.(*http.Transport); ok {
				transport.CloseIdleConnections()
			}
		}
		systemProxyClient = client
		systemProxyURL = currentProxyURL
		return systemProxyClient, nil
	}

	clientLock.RLock()
	if !useProxy && systemDirectClient != nil {
		clientLock.RUnlock()
		return systemDirectClient, nil
	}
	clientLock.RUnlock()

	clientLock.Lock()
	defer clientLock.Unlock()

	if systemDirectClient != nil {
		return systemDirectClient, nil
	}
	client, err := newHTTPClientNoProxy()
	if err != nil {
		return nil, err
	}
	systemDirectClient = client
	return systemDirectClient, nil
}

// GetHTTPClientCustomProxy returns a cached http.Client keyed by the
// normalized proxy URL, so connections are reused across requests
// (keepalive survives; no per-request TCP+TLS+CONNECT handshake).
// proxyURL supports: http, https, socks, socks5
func GetHTTPClientCustomProxy(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return nil, fmt.Errorf("proxy url is empty")
	}
	key, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	if client := proxyClients.get(key); client != nil {
		return client, nil
	}
	client, err := newHTTPClientCustomProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	proxyClients.put(key, client)
	return client, nil
}

// InvalidateProxyClient drops the cached http.Client for the given proxy
// URL (if any), closing its idle connections. Call after a proxy
// configuration's URL changes or the configuration is removed.
func InvalidateProxyClient(proxyURL string) {
	if proxyURL == "" {
		return
	}
	if key, err := normalizeProxyURL(proxyURL); err == nil {
		proxyClients.invalidate(key)
	}
}

func clonedDefaultTransport() (*http.Transport, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("default transport is not *http.Transport")
	}
	cloned := transport.Clone()
	return cloned, nil
}

func newHTTPClientNoProxy() (*http.Client, error) {
	cloned, err := clonedDefaultTransport()
	if err != nil {
		return nil, err
	}
	cloned.Proxy = nil
	return &http.Client{Transport: cloned}, nil
}

func newHTTPClientCustomProxy(proxyURLStr string) (*http.Client, error) {
	cloned, err := clonedDefaultTransport()
	if err != nil {
		return nil, err
	}

	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy url: %w", err)
	}

	switch proxyURL.Scheme {
	case "http", "https":
		cloned.Proxy = http.ProxyURL(proxyURL)
	case "socks", "socks5":
		socksDialer, err := proxy.FromURL(proxyURL, &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("invalid socks proxy: %w", err)
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks proxy does not support cancellation")
		}
		cloned.Proxy = nil
		cloned.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Bound the entire handshake even when the caller has no deadline.
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return contextDialer.DialContext(ctx, network, addr)
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %s", proxyURL.Scheme)
	}

	return &http.Client{Transport: cloned}, nil
}
