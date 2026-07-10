package networking

import (
	"net/http"
	"time"
)

var proxyFromEnvironment = http.ProxyFromEnvironment

// NewTransport returns an independent transport that follows the process proxy environment.
func NewTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport := base.Clone()
		transport.Proxy = proxyFromEnvironment
		return transport
	}
	return &http.Transport{Proxy: proxyFromEnvironment}
}

func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: NewTransport(), Timeout: timeout}
}
