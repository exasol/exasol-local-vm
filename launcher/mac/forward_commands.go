// Copyright 2026 Exasol AG
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

type forwardRequest struct {
	Request   string `json:"request"`
	Name      string `json:"name,omitempty"`
	GuestPort int    `json:"guest_port,omitempty"`
	HostPort  int    `json:"host_port,omitempty"`
	HostIP    string `json:"host_ip,omitempty"`
}

type forwardResponse struct {
	Forwards map[string]ForwardState `json:"forwards"`
	Error    string                  `json:"error,omitempty"`
}

func forwardCmd(args []string, output io.Writer) error {
	request, err := parseForwardCommand(args)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("unix", vmSocketPath, 2*time.Second)
	if err != nil {
		return fmt.Errorf("cannot contact VM; start it before managing forwards: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(statusConnDeadline))
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fmt.Errorf("cannot send forward request: %w", err)
	}
	var response forwardResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return fmt.Errorf("cannot read forward response; ensure the running launcher supports live forwards: %w", err)
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return json.NewEncoder(output).Encode(response)
}

func parseForwardCommand(args []string) (forwardRequest, error) {
	usage := errors.New("usage: launcher forward add [--host-ip <loopback-ip>] <name>:<guest-port>:<host-port> | remove <name> | list")
	if len(args) == 0 {
		return forwardRequest{}, usage
	}
	request := forwardRequest{Request: "forward-" + args[0]}
	switch args[0] {
	case "add":
		flags := flag.NewFlagSet("forward add", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		hostIP := flags.String("host-ip", "127.0.0.1", "Loopback address")
		if err := flags.Parse(args[1:]); err != nil {
			return request, err
		}
		if flags.NArg() != 1 {
			return request, usage
		}
		spec, err := parseForwardSpec(flags.Arg(0))
		if err != nil {
			return request, err
		}
		request.Name, request.GuestPort, request.HostPort = spec.Name, spec.GuestPort, spec.HostPort
		request.HostIP = *hostIP
		if ip := net.ParseIP(request.HostIP); ip == nil || !ip.IsLoopback() {
			return request, errors.New("host IP must be a loopback IP address")
		}
	case "remove":
		if len(args) != 2 {
			return request, usage
		}
		spec, err := parseForwardSpec(args[1] + ":1:0")
		if err != nil {
			return request, err
		}
		request.Name = spec.Name
	case "list":
		if len(args) != 1 {
			return request, usage
		}
	default:
		return request, usage
	}
	return request, nil
}

func handleForwardRequest(request forwardRequest) forwardResponse {
	forwarderRegistryMu.Lock()
	defer forwarderRegistryMu.Unlock()
	if !forwardsReady {
		return forwardResponse{Error: "VM is not ready; wait for a successful start before managing forwards"}
	}
	if err := updateForward(request); err != nil {
		return forwardResponse{Error: err.Error()}
	}
	return forwardResponse{Forwards: forwardStatesLocked()}
}

// The registry lock also orders state publication with socket mutations.
func updateForward(request forwardRequest) error {
	switch request.Request {
	case "forward-list":
		return nil
	case "forward-add":
		spec, err := parseForwardSpec(fmt.Sprintf("%s:%d:%d", request.Name, request.GuestPort, request.HostPort))
		if err != nil {
			return err
		}
		if request.HostIP == "" {
			request.HostIP = "127.0.0.1"
		}
		ip := net.ParseIP(request.HostIP)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("host IP must be a loopback IP address")
		}
		hostIP := ip.String()
		if existing := forwarderRegistry[spec.Name]; existing != nil {
			if existing.guestPort == spec.GuestPort && existing.requestedPort == spec.HostPort && existing.hostIP == hostIP {
				return nil
			}
			return fmt.Errorf("forward %q has different settings; remove it before adding a replacement", spec.Name)
		}
		forwarder, err := startForwarder(context.Background(), spec.Name, hostIP, spec.HostPort, forwardGuestHost, spec.GuestPort)
		if err != nil {
			return err
		}
		forwarderRegistry[spec.Name] = forwarder
		if err := saveForwardState(); err != nil {
			delete(forwarderRegistry, spec.Name)
			forwarder.Close()
			return err
		}
	case "forward-remove":
		forwarder := forwarderRegistry[request.Name]
		if forwarder == nil {
			return nil
		}
		delete(forwarderRegistry, request.Name)
		if err := saveForwardState(); err != nil {
			forwarderRegistry[request.Name] = forwarder
			return err
		}
		return forwarder.Close()
	default:
		return errors.New("unknown forward request")
	}
	return nil
}

func forwardStatesLocked() map[string]ForwardState {
	states := make(map[string]ForwardState, len(forwarderRegistry))
	for name, forwarder := range forwarderRegistry {
		states[name] = ForwardState{HostIP: forwarder.hostIP, GuestPort: forwarder.guestPort, HostPort: forwarder.Port()}
	}
	return states
}

func saveForwardState() error {
	data, err := os.ReadFile("vm-state.json")
	if err != nil {
		return fmt.Errorf("cannot read VM state: %w", err)
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("cannot decode VM state: %w", err)
	}
	if state == nil {
		return errors.New("VM state must be a JSON object")
	}
	state["forwards"], err = json.Marshal(forwardStatesLocked())
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeVMState(data)
}

func writeVMState(data []byte) error {
	file, err := os.CreateTemp(".", ".vm-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(0644); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), "vm-state.json")
}

func closeForwarders() {
	forwarderRegistryMu.Lock()
	defer forwarderRegistryMu.Unlock()
	forwardsReady = false
	forwardsStopping = true
	for name, forwarder := range forwarderRegistry {
		forwarder.Close()
		delete(forwarderRegistry, name)
	}
}
