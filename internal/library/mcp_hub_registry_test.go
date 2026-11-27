package library

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// The renamed test binary acts as a package manager without running one.
// Its tool list identifies the environment it had when Magpie started it.
func TestFakeRegistryHubServer(t *testing.T) {
	if os.Getenv("MAGPIE_FAKE_REGISTRY_HUB") != "1" {
		t.Skip("run by the router registry test")
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m rpcMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"fake","version":"1"}}}`+"\n", m.ID)
		case "tools/list":
			b, _ := json.Marshal([]Tool{{Name: "env", Description: os.Getenv("NPM_CONFIG_REGISTRY") + " " + os.Getenv("MCP_TOKEN")}})
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":%s}}`+"\n", m.ID, b)
		}
	}
	// End the subprocess directly, as the other fake MCP servers do.
	os.Exit(0)
}

// The router starts an affected server again with the new upstream, not
// an unrelated server, and never describes the replacement using a tool
// list from the previous registry.
func TestHubNPMRegistryRestartsOnlyAffectedBackends(t *testing.T) {
	sandbox(t)
	t.Setenv("NPM_CONFIG_REGISTRY", "https://ambient.example/")
	first, second := "https://first.example/npm", "https://second.example/npm"
	setMCPTestRegistry(t, first)
	a := fakeStdio(t, "ok", nil)
	a.Name = "arbitrary"
	s := &Server{
		Name: "managed", Transport: "stdio", Command: fakeManager(t, "npm"),
		Args: []string{"-test.run=^TestFakeRegistryHubServer$"},
		Env:  map[string]string{"MAGPIE_FAKE_REGISTRY_HUB": "1", "MCP_TOKEN": "kept"},
	}
	// End the children before the fake command's TempDir is removed.
	t.Cleanup(hubStopAll)
	o := *s
	o.Name = "override"
	o.Env = map[string]string{"MAGPIE_FAKE_REGISTRY_HUB": "1", "MCP_TOKEN": "own", "NPM_CONFIG_REGISTRY": "https://own.example/"}
	ok(t)(changeMCP(func(l *Library) error {
		l.MCP, l.MCPHub = append(l.MCP, s, a, &o), true
		return nil
	}))
	h := theHub{}
	listed := func() map[string]string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		m := map[string]string{}
		for _, tool := range h.Tools(ctx, "") {
			var desc string
			if err := json.Unmarshal(tool.Spec["description"], &desc); err == nil {
				m[tool.Server] = desc
			}
		}
		return m
	}
	if got := listed(); got[s.Name] != first+"/ kept" || got[o.Name] != "https://own.example/ own" {
		t.Fatalf("initial router tool descriptions = %v", got)
	}
	original, unaffected, overridden := backendOf(s), backendOf(a), backendOf(&o)
	setMCPTestRegistry(t, second)
	hubPrune()
	for _, b := range []*backend{original, unaffected, overridden} {
		b.mu.Lock()
		stopped := b.stopped
		b.mu.Unlock()
		if stopped != (b == original) {
			t.Errorf("server %q stopped = %v after registry changed", b.s.Name, stopped)
		}
	}
	if cardOf(s) != nil || cardOf(a) == nil || cardOf(&o) == nil {
		t.Fatal("the registry change should invalidate only the affected server's card")
	}
	if got := listed(); got[s.Name] != second+"/ kept" || got[o.Name] != "https://own.example/ own" {
		t.Fatalf("router tool descriptions after switch = %v", got)
	}
	noteCard(s, original.key, serverSays{Title: "Late old reply"}, nil)
	if got := cardOf(s); got == nil || len(got.Tools) != 1 || got.Tools[0].Description != second+"/ kept" {
		t.Fatalf("late answer from original backend replaced the current card: %+v", got)
	}
	setMCPTestRegistry(t, "")
	hubPrune()
	if got := listed(); got[s.Name] != "https://ambient.example/ kept" {
		t.Errorf("router tool descriptions after clearing the registry = %v", got)
	}
}
