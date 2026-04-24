//go:build !js

package grpc

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	nbnet "github.com/netbirdio/netbird/client/net"
)

var androidResolverLogOnce sync.Once

func WithCustomDialer(_ bool, _ string) grpc.DialOption {
	return grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
		if runtime.GOOS == "linux" {
			currentUser, err := user.Current()
			if err != nil {
				return nil, status.Errorf(codes.FailedPrecondition, "failed to get current user: %v", err)
			}

			// the custom dialer requires root permissions which are not required for use cases run as non-root
			if currentUser.Uid != "0" {
				log.Debug("Not running as root, using standard dialer")
				dialer := &net.Dialer{}
				return dialer.DialContext(ctx, "tcp", addr)
			}
		}

		dialer := nbnet.NewDialer()
		if runtime.GOOS == "android" {
			start := time.Now()
			conn, ok, err := nbnet.DialAndroidTCP(ctx, addr)
			if ok {
				if err != nil {
					log.Errorf("Android native gRPC dial to %s failed after %s: %v", addr, time.Since(start), err)
					return nil, err
				}
				log.Infof("Android native gRPC dial to %s connected in %s from %s to %s", addr, time.Since(start), conn.LocalAddr(), conn.RemoteAddr())
				return conn, nil
			}
			if resolver := androidResolverFromEnv(); resolver != nil {
				dialer.Resolver = resolver
			}
			if conn, ok, err := dialAndroidHostOverride(ctx, dialer, addr); ok {
				return conn, err
			}
		}

		start := time.Now()
		if runtime.GOOS == "android" {
			log.Infof("Android gRPC dialing %s", addr)
		}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			if runtime.GOOS == "android" {
				log.Errorf("Android gRPC dial to %s failed after %s: %v", addr, time.Since(start), err)
			}
			return nil, fmt.Errorf("nbnet.NewDialer().DialContext: %w", err)
		}
		if runtime.GOOS == "android" {
			log.Infof("Android gRPC dial to %s connected in %s from %s to %s", addr, time.Since(start), conn.LocalAddr(), conn.RemoteAddr())
		}
		return conn, nil
	})
}

func dialAndroidHostOverride(ctx context.Context, dialer *nbnet.Dialer, addr string) (net.Conn, bool, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, false, nil
	}

	ips := androidHostOverridesFromEnv()[canonicalAndroidHost(host)]
	if len(ips) == 0 {
		return nil, false, nil
	}

	var lastErr error
	for _, ip := range ips {
		dialAddr := net.JoinHostPort(ip, port)
		start := time.Now()
		log.Infof("Android gRPC dialing %s via resolved IP %s", addr, dialAddr)
		conn, err := dialer.DialContext(ctx, "tcp", dialAddr)
		if err == nil {
			log.Infof("Android gRPC dial to %s via %s connected in %s from %s to %s", addr, dialAddr, time.Since(start), conn.LocalAddr(), conn.RemoteAddr())
			return conn, true, nil
		}
		log.Errorf("Android gRPC dial to %s via %s failed after %s: %v", addr, dialAddr, time.Since(start), err)
		lastErr = err
	}

	return nil, true, fmt.Errorf("all Android host override dials failed for %s: %w", addr, lastErr)
}

func androidHostOverridesFromEnv() map[string][]string {
	overrides := make(map[string][]string)
	for _, entry := range strings.Split(os.Getenv("NB_ANDROID_DNS_HOSTS"), ";") {
		host, rawIPs, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		host = canonicalAndroidHost(host)
		if host == "" {
			continue
		}
		for _, rawIP := range strings.Split(rawIPs, "|") {
			ip := strings.TrimSpace(rawIP)
			if net.ParseIP(ip) == nil {
				continue
			}
			overrides[host] = append(overrides[host], ip)
		}
	}
	return overrides
}

func canonicalAndroidHost(host string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".[]")
}

func androidResolverFromEnv() *net.Resolver {
	rawServers := strings.Split(os.Getenv("NB_ANDROID_DNS_SERVERS"), ",")
	servers := make([]string, 0, len(rawServers))
	for _, server := range rawServers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(server); err == nil {
			servers = append(servers, server)
		} else {
			servers = append(servers, net.JoinHostPort(server, "53"))
		}
	}
	if len(servers) == 0 {
		return nil
	}

	androidResolverLogOnce.Do(func() {
		log.Infof("Android DNS resolver using %s", strings.Join(servers, ","))
	})

	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for _, server := range servers {
				dialer := net.Dialer{Timeout: 5 * time.Second}
				start := time.Now()
				conn, err := dialer.DialContext(ctx, network, server)
				if err == nil {
					log.Debugf("Android DNS resolver dialed %s %s in %s", network, server, time.Since(start))
					return conn, nil
				}
				log.Debugf("Android DNS resolver failed %s %s after %s: %v", network, server, time.Since(start), err)
				lastErr = err
			}
			return nil, lastErr
		},
	}
}
