// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const vmSocketPath = "vm.sock"

type ForwardSpec struct {
	Name      string
	GuestPort int
	HostPort  int
}

// ForwardState is the public forwarding result stored in vm-state.json.
type ForwardState struct {
	HostIP    string `json:"host_ip"`
	GuestPort int    `json:"guest_port"`
	HostPort  int    `json:"host_port"`
}

type forwardSpecList []ForwardSpec

func (list forwardSpecList) String() string {
	parts := make([]string, 0, len(list))
	for _, spec := range list {
		parts = append(parts, encodeForwardSpec(spec))
	}

	return strings.Join(parts, ",")
}

func (list *forwardSpecList) Set(value string) error {
	spec, err := parseForwardSpec(value)
	if err != nil {
		return err
	}
	for _, existing := range *list {
		if existing.Name == spec.Name {
			return fmt.Errorf("forward name %q is configured more than once", spec.Name)
		}
	}
	*list = append(*list, spec)

	return nil
}

type LoopbackForwarder struct {
	hostIP        string
	requestedPort int
	cancel        context.CancelFunc
	name          string
	listener      net.Listener
	guestHost     string
	guestPort     int
	closeOnce     sync.Once
	closeError    error
	wg            sync.WaitGroup
}

func StartLoopbackForwarder(ctx context.Context, name string, hostPort int, guestHost string, guestPort int) (*LoopbackForwarder, error) {
	return startForwarder(ctx, name, "127.0.0.1", hostPort, guestHost, guestPort)
}

func startForwarder(ctx context.Context, name, hostIP string, hostPort int, guestHost string, guestPort int) (*LoopbackForwarder, error) {
	listener, err := (&net.ListenConfig{}).Listen(
		ctx,
		"tcp",
		net.JoinHostPort(hostIP, strconv.Itoa(hostPort)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s:%d: %w", hostIP, hostPort, err)
	}
	ctx, cancel := context.WithCancel(ctx)

	forwarder := &LoopbackForwarder{
		hostIP:        hostIP,
		requestedPort: hostPort,
		cancel:        cancel,
		name:          name,
		listener:      listener,
		guestHost:     guestHost,
		guestPort:     guestPort,
	}
	forwarder.wg.Add(1)
	go forwarder.acceptLoop(ctx)

	return forwarder, nil
}

// classifyDialErr turns a raw dial error into the small state vocabulary
// ("reachable", "refused", "blocked", or "timeout") reported over the status
// socket, rather than exposing OS-specific errors.
func classifyDialErr(err error) string {
	if err == nil {
		return "reachable"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return "blocked"
	}

	// Unrecognized failure: signal a problem rather than silently
	// reporting reachable.
	return "blocked"
}

// Probe dials the guest address on its own, independent of any real client
// connection, so health-check can report state even when nothing is
// currently forwarding traffic through this port.
func (f *LoopbackForwarder) Probe(ctx context.Context, timeout time.Duration) string {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", fmt.Sprintf("%s:%d", f.guestHost, f.guestPort))
	if err == nil {
		conn.Close()
	}

	return classifyDialErr(err)
}

func (f *LoopbackForwarder) Port() int {
	if addr, ok := f.listener.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

func (f *LoopbackForwarder) Close() error {
	f.closeOnce.Do(func() {
		f.cancel()
		f.closeError = f.listener.Close()
		f.wg.Wait()
	})

	if f.closeError != nil && !errors.Is(f.closeError, net.ErrClosed) {
		return f.closeError
	}

	return nil
}

func (f *LoopbackForwarder) acceptLoop(ctx context.Context) {
	defer f.wg.Done()

	for {
		clientConn, err := f.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}

		f.wg.Add(1)
		go f.proxyConnection(ctx, clientConn)
	}
}

func (f *LoopbackForwarder) proxyConnection(ctx context.Context, clientConn net.Conn) {
	defer f.wg.Done()
	defer clientConn.Close()
	stopClient := context.AfterFunc(ctx, func() { clientConn.Close() })
	defer stopClient()

	guestConn, err := (&net.Dialer{}).DialContext(
		ctx,
		"tcp",
		fmt.Sprintf("%s:%d", f.guestHost, f.guestPort),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] Warning: %s forwarder could not reach guest %s:%d (%s): %v\n",
			time.Now().Format("15:04:05"), f.name, f.guestHost, f.guestPort, classifyDialErr(err), err)
		return
	}
	defer guestConn.Close()
	stopGuest := context.AfterFunc(ctx, func() { guestConn.Close() })
	defer stopGuest()

	var copyWG sync.WaitGroup
	copyWG.Add(1)

	go func() {
		defer copyWG.Done()
		io.Copy(guestConn, clientConn)
		guestConn.Close()
	}()

	io.Copy(clientConn, guestConn)
	clientConn.Close()
	copyWG.Wait()
}

func parseForwardSpec(value string) (ForwardSpec, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 {
		return ForwardSpec{}, fmt.Errorf(
			"invalid forward %q: expected <name>:<guest-port>:<host-port>", value,
		)
	}

	name := strings.TrimSpace(parts[0])
	if name == "" {
		return ForwardSpec{}, fmt.Errorf("invalid forward %q: name must not be empty", value)
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}

		return ForwardSpec{}, fmt.Errorf(
			"invalid forward %q: name may contain only letters, digits, '.', '_' and '-'", value,
		)
	}

	guestPort, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || guestPort < 1 || guestPort > 65535 {
		return ForwardSpec{}, fmt.Errorf(
			"invalid forward %q: guest port must be an integer from 1 to 65535", value,
		)
	}
	hostPort, err := strconv.Atoi(strings.TrimSpace(parts[2]))
	if err != nil || hostPort < 0 || hostPort > 65535 {
		return ForwardSpec{}, fmt.Errorf(
			"invalid forward %q: host port must be an integer from 0 to 65535", value,
		)
	}

	return ForwardSpec{Name: name, GuestPort: guestPort, HostPort: hostPort}, nil
}

func encodeForwardSpec(spec ForwardSpec) string {
	return fmt.Sprintf("%s:%d:%d", spec.Name, spec.GuestPort, spec.HostPort)
}

func parseForwardSpecs(value string) ([]ForwardSpec, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}

	var specs forwardSpecList
	for _, entry := range strings.Split(value, ",") {
		if err := specs.Set(entry); err != nil {
			return nil, err
		}
	}

	return specs, nil
}

const (
	// healthCheckPerPortTimeout bounds a single forwarder's probe dial, so a
	// blocked/hanging guest connection cannot stall the whole health-check.
	healthCheckPerPortTimeout = 2 * time.Second
	// healthCheckConnDeadline bounds the whole health-check request/response,
	// generous enough for every forwarder's probe to run concurrently and
	// still finish comfortably inside it.
	healthCheckConnDeadline = 10 * time.Second
	// statusConnDeadline bounds the cheap, non-probing "status" request.
	statusConnDeadline = 5 * time.Second
)

var (
	forwarderRegistryMu sync.RWMutex
	forwarderRegistry   = map[string]*LoopbackForwarder{}
	forwardGuestHost    string
	forwardsReady       bool
	forwardsStopping    bool
)

// registerForwarder makes a forwarder visible to health-check requests on
// the status socket, keyed by its service name (e.g. "ssh").
func registerForwarder(name string, forwarder *LoopbackForwarder) {
	forwarderRegistryMu.Lock()
	defer forwarderRegistryMu.Unlock()
	forwarderRegistry[name] = forwarder
}

func forwarderSnapshot() map[string]*LoopbackForwarder {
	forwarderRegistryMu.RLock()
	defer forwarderRegistryMu.RUnlock()

	return maps.Clone(forwarderRegistry)
}

// portHealthResponse is the per-port shape returned by a health-check request.
type portHealthResponse struct {
	State string `json:"state"`
}

// probeForwarders always dials fresh rather than returning each forwarder's
// last-observed state, so a port nothing has recently connected through
// (e.g. SSH during a plain start/connect) still gets a current answer.
func probeForwarders(ctx context.Context) map[string]portHealthResponse {
	snapshot := forwarderSnapshot()
	result := make(map[string]portHealthResponse, len(snapshot))

	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, forwarder := range snapshot {
		wg.Add(1)
		go func(name string, forwarder *LoopbackForwarder) {
			defer wg.Done()
			state := forwarder.Probe(ctx, healthCheckPerPortTimeout)
			mu.Lock()
			result[name] = portHealthResponse{State: state}
			mu.Unlock()
		}(name, forwarder)
	}
	wg.Wait()

	return result
}
