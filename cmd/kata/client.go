package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wesm/kata/internal/daemon"
)

// ensureDaemon discovers a live daemon's HTTP base URL, auto-starting one if
// none is found.
func ensureDaemon(ctx context.Context) (string, error) {
	ns, err := daemon.NewNamespace()
	if err != nil {
		return "", err
	}
	if url, ok := tryDiscover(ctx, ns.DataDir); ok {
		return url, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	//nolint:gosec // G204: exe is os.Executable()
	cmd := exec.Command(exe, "daemon", "start")
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("auto-start daemon: %w", err)
	}
	go func() { _ = cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if url, ok := tryDiscover(ctx, ns.DataDir); ok {
			return url, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", errors.New("daemon failed to start within 5s")
}

func tryDiscover(ctx context.Context, dataDir string) (string, bool) {
	recs, err := daemon.ListRuntimeFiles(dataDir)
	if err != nil {
		return "", false
	}
	for _, r := range recs {
		if !daemon.ProcessAlive(r.PID) {
			continue
		}
		url, ok := pingAddress(ctx, r.Address)
		if ok {
			return url, true
		}
	}
	return "", false
}

func pingAddress(ctx context.Context, address string) (string, bool) {
	if strings.HasPrefix(address, "unix://") {
		path := strings.TrimPrefix(address, "unix://")
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", path)
				},
			},
			Timeout: 1 * time.Second,
		}
		const base = "http://kata"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/ping", nil)
		if err != nil {
			return "", false
		}
		resp, err := client.Do(req) //nolint:gosec // G704: address comes from our own runtime file, not user input
		if err != nil {
			return "", false
		}
		_ = resp.Body.Close()
		return base, true
	}
	url := "http://" + address
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/ping", nil)
	if err != nil {
		return "", false
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // G704: address comes from our own runtime file, not user input
	if err != nil {
		return "", false
	}
	_ = resp.Body.Close()
	return url, true
}

// httpClientFor returns an *http.Client whose transport understands unix://
// addresses. Pair with the URL returned by ensureDaemon.
//
//nolint:unused // consumed by upcoming command implementations (Tasks 22-27)
func httpClientFor(baseURL string) (*http.Client, error) {
	if !strings.HasPrefix(baseURL, "http://kata") {
		return &http.Client{Timeout: 5 * time.Second}, nil
	}
	ns, err := daemon.NewNamespace()
	if err != nil {
		return nil, err
	}
	recs, err := daemon.ListRuntimeFiles(ns.DataDir)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if strings.HasPrefix(r.Address, "unix://") {
			path := strings.TrimPrefix(r.Address, "unix://")
			return &http.Client{
				Transport: &http.Transport{
					DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
						return (&net.Dialer{}).DialContext(ctx, "unix", path)
					},
				},
				Timeout: 5 * time.Second,
			}, nil
		}
	}
	return nil, errors.New("no unix-socket daemon found")
}
