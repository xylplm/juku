package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type safeDNSDialer struct {
	*dnsResolver
	dialer net.Dialer
}

func newSafeDNSDialer(transport *http.Transport) *safeDNSDialer {
	return &safeDNSDialer{
		dnsResolver: newDNSResolver(transport),
		dialer:      net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
}

func protectedCDNHost(host string) bool {
	host = strings.ToLower(host)
	return isHuangguoImageCDNHost(host) || strings.HasSuffix(host, ".lkkwip.cn")
}

func (resolver *safeDNSDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !protectedCDNHost(host) {
		return resolver.dialer.DialContext(ctx, network, address)
	}
	addresses, err := resolver.lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%s 安全 DNS 解析失败，可在网络设置中启用可用代理: %w", host, err)
	}
	var lastErr error
	for _, resolved := range addresses {
		connection, err := resolver.dialer.DialContext(ctx, network, net.JoinHostPort(resolved, port))
		if err == nil {
			return connection, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%s 的 CDN 无法直连，请检查代理配置: %w", host, lastErr)
}
