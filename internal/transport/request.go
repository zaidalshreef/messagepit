// Package transport contains checks shared by HTTP transport boundaries.
package transport

import (
	"net"
	"net/http"
)

// SecureOrLoopback allows TLS and direct loopback traffic. A TLS-terminating
// proxy may connect over loopback; forwarded headers are not trusted here.
func SecureOrLoopback(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
