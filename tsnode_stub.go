//go:build notsnet

package main

// Stand-in used only when building with -tags notsnet (offline development
// without the Tailscale library). Real builds use tsnode.go.
//
// With ENSEMBLE_FAKE_TAILSCALE=1 it simulates a device that asks you to sign
// in, then connects a few seconds later, so the screens can be tried out.

import (
	"context"
	"errors"
	"net"
	"os"
	"time"
)

func newRealTSNode(dir, hostname string, logf func(string, ...any)) (tsNode, error) {
	if os.Getenv("ENSEMBLE_FAKE_TAILSCALE") == "1" {
		return &demoNode{start: time.Now()}, nil
	}
	return nil, errors.New("this build was made without Tailscale")
}

type demoNode struct{ start time.Time }

func (d *demoNode) State(context.Context) (TSState, error) {
	if time.Since(d.start) < 6*time.Second {
		return TSState{BackendState: "NeedsLogin", AuthURL: "https://login.tailscale.com/a/demo"}, nil
	}
	return TSState{BackendState: "Running", DNSName: "ensemble.tail1234.ts.net", IPv4: "127.0.0.3", Tailnet: "you@gmail.com", HTTPS: true}, nil
}
func (d *demoNode) Login(context.Context) error { return nil }
func (d *demoNode) Listen(bool) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.3:4319")
}
func (d *demoNode) Logout(context.Context) error { return nil }
func (d *demoNode) Close() error                 { return nil }
