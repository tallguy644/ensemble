//go:build !notsnet

package main

// The real built-in Tailscale device, using Tailscale's tsnet library.

import (
	"context"
	"net"
	"strings"

	"tailscale.com/tsnet"
)

type realTSNode struct {
	s *tsnet.Server
}

func newRealTSNode(dir, hostname string, logf func(string, ...any)) (tsNode, error) {
	s := &tsnet.Server{
		Dir:      dir,
		Hostname: hostname,
		Logf:     logf,
		UserLogf: logf,
	}
	if err := s.Start(); err != nil {
		return nil, err
	}
	return &realTSNode{s: s}, nil
}

func (n *realTSNode) State(ctx context.Context) (TSState, error) {
	lc, err := n.s.LocalClient()
	if err != nil {
		return TSState{}, err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return TSState{}, err
	}
	out := TSState{BackendState: st.BackendState, AuthURL: st.AuthURL, HTTPS: len(st.CertDomains) > 0}
	if st.Self != nil {
		out.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		for _, ip := range st.Self.TailscaleIPs {
			if ip.Is4() {
				out.IPv4 = ip.String()
				break
			}
		}
	}
	if st.CurrentTailnet != nil {
		out.Tailnet = st.CurrentTailnet.Name
	}
	return out, nil
}

func (n *realTSNode) Login(ctx context.Context) error {
	lc, err := n.s.LocalClient()
	if err != nil {
		return err
	}
	return lc.StartLoginInteractive(ctx)
}

func (n *realTSNode) Listen(https bool) (net.Listener, error) {
	if https {
		return n.s.ListenTLS("tcp", ":443")
	}
	return n.s.Listen("tcp", ":80")
}

func (n *realTSNode) Logout(ctx context.Context) error {
	lc, err := n.s.LocalClient()
	if err != nil {
		return err
	}
	return lc.Logout(ctx)
}

func (n *realTSNode) Close() error { return n.s.Close() }
