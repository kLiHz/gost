package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/go-gost/gost/pkg/config"
	"github.com/go-gost/gost/pkg/metadata"
	"github.com/go-gost/gost/pkg/registry"
)

var (
	ErrInvalidCmd  = errors.New("invalid cmd")
	ErrInvalidNode = errors.New("invalid node")
)

type stringList []string

func (l *stringList) String() string {
	return fmt.Sprintf("%s", *l)
}
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func buildConfigFromCmd(services, nodes stringList) (*config.Config, error) {
	cfg := &config.Config{}

	var chain *config.ChainConfig
	if len(nodes) > 0 {
		chain = &config.ChainConfig{
			Name: "chain-0",
		}
		cfg.Chains = append(cfg.Chains, chain)
	}

	for i, node := range nodes {
		url, err := normCmd(node)
		if err != nil {
			return nil, err
		}

		nodeConfig, err := buildNodeConfig(url)
		if err != nil {
			return nil, err
		}
		nodeConfig.Name = "node-0"

		md := metadata.MapMetadata(nodeConfig.Connector.Metadata)
		if v := metadata.GetString(md, "bypass"); v != "" {
			bypassCfg := &config.BypassConfig{
				Name: fmt.Sprintf("bypass-%d", len(cfg.Bypasses)),
			}
			if v[0] == '~' {
				bypassCfg.Reverse = true
				v = v[1:]
			}
			for _, s := range strings.Split(v, ",") {
				if s == "" {
					continue
				}
				bypassCfg.Matchers = append(bypassCfg.Matchers, s)
			}
			nodeConfig.Bypass = bypassCfg.Name
			cfg.Bypasses = append(cfg.Bypasses, bypassCfg)
			md.Del("bypass")
		}

		var nodes []*config.NodeConfig
		if v := metadata.GetString(md, "ip"); v != "" {
			_, port, err := net.SplitHostPort(nodeConfig.Addr)
			if err != nil {
				return nil, err
			}
			ips := parseIP(v, port)
			for i := range ips {
				nodeCfg := &config.NodeConfig{}
				*nodeCfg = *nodeConfig
				nodeCfg.Name = fmt.Sprintf("node-%d", i)
				nodeCfg.Addr = ips[i]
				nodes = append(nodes, nodeCfg)
			}
			md.Del("ip")
		}
		if len(nodes) == 0 {
			nodes = append(nodes, nodeConfig)
		}

		chain.Hops = append(chain.Hops, &config.HopConfig{
			Name:  fmt.Sprintf("hop-%d", i),
			Nodes: nodes,
		})
	}

	for i, svc := range services {
		url, err := normCmd(svc)
		if err != nil {
			return nil, err
		}

		service, err := buildServiceConfig(url)
		if err != nil {
			return nil, err
		}
		service.Name = fmt.Sprintf("service-%d", i)
		if chain != nil {
			if service.Listener.Type == "rtcp" || service.Listener.Type == "rudp" {
				service.Listener.Chain = chain.Name
			} else {
				service.Handler.Chain = chain.Name
			}
		}
		cfg.Services = append(cfg.Services, service)

		md := metadata.MapMetadata(service.Handler.Metadata)
		if v := metadata.GetString(md, "bypass"); v != "" {
			bypassCfg := &config.BypassConfig{
				Name: fmt.Sprintf("bypass-%d", len(cfg.Bypasses)),
			}
			if v[0] == '~' {
				bypassCfg.Reverse = true
				v = v[1:]
			}
			for _, s := range strings.Split(v, ",") {
				if s == "" {
					continue
				}
				bypassCfg.Matchers = append(bypassCfg.Matchers, s)
			}
			service.Handler.Bypass = bypassCfg.Name
			cfg.Bypasses = append(cfg.Bypasses, bypassCfg)
			md.Del("bypass")
		}
		if v := metadata.GetString(md, "resolver"); v != "" {
			resolverCfg := &config.ResolverConfig{
				Name: fmt.Sprintf("resolver-%d", len(cfg.Resolvers)),
			}
			for _, rs := range strings.Split(v, ",") {
				if rs == "" {
					continue
				}
				resolverCfg.Nameservers = append(
					resolverCfg.Nameservers,
					config.NameserverConfig{
						Addr: rs,
					},
				)
			}
			service.Handler.Resolver = resolverCfg.Name
			cfg.Resolvers = append(cfg.Resolvers, resolverCfg)
			md.Del("resolver")
		}
		if v := metadata.GetString(md, "hosts"); v != "" {
			hostsCfg := &config.HostsConfig{
				Name: fmt.Sprintf("hosts-%d", len(cfg.Hosts)),
			}
			for _, s := range strings.Split(v, ",") {
				ss := strings.SplitN(s, ":", 2)
				if len(ss) != 2 {
					continue
				}
				hostsCfg.Mappings = append(
					hostsCfg.Mappings,
					config.HostMappingConfig{
						Hostname: ss[0],
						IP:       ss[1],
					},
				)
			}
			service.Handler.Hosts = hostsCfg.Name
			cfg.Hosts = append(cfg.Hosts, hostsCfg)
			md.Del("hosts")
		}

	}

	return cfg, nil
}

func buildServiceConfig(url *url.URL) (*config.ServiceConfig, error) {
	var handler, listener string
	schemes := strings.Split(url.Scheme, "+")
	if len(schemes) == 1 {
		handler = schemes[0]
		listener = schemes[0]
	}
	if len(schemes) == 2 {
		handler = schemes[0]
		listener = schemes[1]
	}

	svc := &config.ServiceConfig{
		Addr: url.Host,
	}

	if h := registry.GetHandler(handler); h == nil {
		handler = "auto"
	}
	if ln := registry.GetListener(listener); ln == nil {
		listener = "tcp"
		if handler == "ssu" {
			listener = "udp"
		}
	}

	if remotes := strings.Trim(url.EscapedPath(), "/"); remotes != "" {
		svc.Forwarder = &config.ForwarderConfig{
			Targets: strings.Split(remotes, ","),
		}
		if handler != "relay" {
			if listener == "tcp" || listener == "udp" ||
				listener == "rtcp" || listener == "rudp" ||
				listener == "tun" || listener == "tap" {
				handler = listener
			} else {
				handler = "tcp"
			}
		}
	}

	var auths []*config.AuthConfig
	if url.User != nil {
		auth := &config.AuthConfig{
			Username: url.User.Username(),
		}
		auth.Password, _ = url.User.Password()
		auths = append(auths, auth)
	}

	md := metadata.MapMetadata{}
	for k, v := range url.Query() {
		if len(v) > 0 {
			md[k] = v[0]
		}
	}

	if sa := metadata.GetString(md, "auth"); sa != "" {
		au, err := parseAuthFromCmd(sa)
		if err != nil {
			return nil, err
		}
		auths = append(auths, au)
	}
	md.Del("auth")

	tlsConfig := &config.TLSConfig{
		CertFile: metadata.GetString(md, "certFile"),
		KeyFile:  metadata.GetString(md, "keyFile"),
		CAFile:   metadata.GetString(md, "caFile"),
	}
	md.Del("certFile")
	md.Del("keyFile")
	md.Del("caFile")

	if tlsConfig.CertFile == "" {
		tlsConfig = nil
	}

	if v := metadata.GetString(md, "dns"); v != "" {
		md.Set("dns", strings.Split(v, ","))
	}

	svc.Handler = &config.HandlerConfig{
		Type:     handler,
		Auths:    auths,
		Metadata: md,
	}
	svc.Listener = &config.ListenerConfig{
		Type:     listener,
		TLS:      tlsConfig,
		Metadata: md,
	}

	return svc, nil
}

func buildNodeConfig(url *url.URL) (*config.NodeConfig, error) {
	var connector, dialer string
	schemes := strings.Split(url.Scheme, "+")
	if len(schemes) == 1 {
		connector = schemes[0]
		dialer = schemes[0]
	}
	if len(schemes) == 2 {
		connector = schemes[0]
		dialer = schemes[1]
	}

	node := &config.NodeConfig{
		Addr: url.Host,
	}

	if c := registry.GetConnector(connector); c == nil {
		connector = "http"
	}
	if d := registry.GetDialer(dialer); d == nil {
		dialer = "tcp"
		if connector == "ssu" {
			dialer = "udp"
		}
	}

	var auth *config.AuthConfig
	if url.User != nil {
		auth = &config.AuthConfig{
			Username: url.User.Username(),
		}
		auth.Password, _ = url.User.Password()
	}

	md := metadata.MapMetadata{}
	for k, v := range url.Query() {
		if len(v) > 0 {
			md[k] = v[0]
		}
	}

	if sauth := metadata.GetString(md, "auth"); sauth != "" && auth == nil {
		au, err := parseAuthFromCmd(sauth)
		if err != nil {
			return nil, err
		}
		auth = au
	}
	md.Del("auth")

	tlsConfig := &config.TLSConfig{
		CAFile:     metadata.GetString(md, "caFile"),
		Secure:     metadata.GetBool(md, "secure"),
		ServerName: metadata.GetString(md, "serverName"),
	}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = url.Hostname()
	}
	md.Del("caFile")
	md.Del("secure")
	md.Del("serverName")

	if !tlsConfig.Secure && tlsConfig.CAFile == "" {
		tlsConfig = nil
	}

	node.Connector = &config.ConnectorConfig{
		Type:     connector,
		Auth:     auth,
		Metadata: md,
	}
	node.Dialer = &config.DialerConfig{
		Type:     dialer,
		TLS:      tlsConfig,
		Metadata: md,
	}

	return node, nil
}

func normCmd(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrInvalidCmd
	}

	if s[0] == ':' || !strings.Contains(s, "://") {
		s = "auto://" + s
	}

	url, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if url.Scheme == "https" {
		url.Scheme = "http+tls"
	}

	return url, nil
}

func parseAuthFromCmd(sa string) (*config.AuthConfig, error) {
	v, err := base64.StdEncoding.DecodeString(sa)
	if err != nil {
		return nil, err
	}
	cs := string(v)
	n := strings.IndexByte(cs, ':')
	if n < 0 {
		return &config.AuthConfig{
			Username: cs,
		}, nil
	}

	return &config.AuthConfig{
		Username: cs[:n],
		Password: cs[n+1:],
	}, nil
}

func parseIP(s, port string) (ips []string) {
	if s == "" {
		return nil
	}
	if port == "" {
		port = "8080"
	}

	for _, v := range strings.Split(s, ",") {
		if v == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(v); err != nil {
			v = net.JoinHostPort(v, port) // assume the port is missing
		}
		ips = append(ips, v)
	}

	return
}
