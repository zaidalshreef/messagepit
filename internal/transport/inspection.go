package transport

import (
	"context"
	"errors"
	"net"

	"github.com/coreydaley/messagepit/internal/tools"
)

// ErrInternalAddress identifies an inspection request blocked by network policy.
var ErrInternalAddress = errors.New("blocked request to private/reserved address")

// InspectionDialContext resolves a host once, checks every result, and dials the
// checked IP. This prevents DNS rebinding from bypassing the internal IP policy.
func InspectionDialContext(dialer *net.Dialer, allowInternal bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}

		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("hostname has no addresses")
		}
		if !allowInternal {
			for _, ip := range ips {
				if tools.IsInternalIP(ip.IP) {
					return nil, ErrInternalAddress
				}
			}
		}

		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}
