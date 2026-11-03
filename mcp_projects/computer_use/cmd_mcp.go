//go:build windows

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"computeruse/internal/mcp"
)

// runMCP serves the Model Context Protocol on stdin and stdout, in front of
// the running app. It starts the app when asked to, or when it is not
// running and --spawn is given.
func runMCP(args []string) error {
	base := "http://" + defaultListen
	if v, ok := flagValue(args, "url"); ok && v != "" {
		base = v
	}
	token := os.Getenv("COMPUTER_USE_TOKEN")
	if v, ok := flagValue(args, "token"); ok {
		token = v
	}
	if !healthy(base, token) {
		if flagBool(args, "spawn") {
			if err := spawnApp(base); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("the app is not running at %s: start `computer-use run`, or pass --spawn", base)
		}
	}
	srv := mcp.New(base, token, version, os.Stdin, os.Stdout)
	return srv.Serve()
}

// healthy asks the app whether it is there.
func healthy(base, token string) bool {
	req, err := http.NewRequest("GET", strings.TrimSuffix(base, "/")+"/v1/health", nil)
	if err != nil {
		return false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// spawnApp starts the app detached and waits until it answers.
func spawnApp(base string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	addr := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	if i := strings.Index(addr, "/"); i >= 0 {
		addr = addr[:i]
	}
	cmd := exec.Command(exe, "run", "--listen", addr)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// No console of its own, out of this process's group: the app
		// outlives the MCP session if the client goes away.
		CreationFlags: 0x00000008 | 0x00000200, // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start the app: %w", err)
	}
	// Release the child: it runs on its own.
	_ = cmd.Process.Release()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(base, os.Getenv("COMPUTER_USE_TOKEN")) {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the app did not answer at %s within 25 s", base)
}
