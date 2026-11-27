package library

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/testenv"
)

func setMCPTestRegistry(t *testing.T, registry string) {
	t.Helper()
	s := settings.Load()
	s.NPMRegistry = registry
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
}

func TestMCPDirectNPMRegistryFollowsSettings(t *testing.T) {
	h := sandbox(t)
	cfg := filepath.Join(h, ".codex", "config.toml")
	servers := []*Server{
		{Name: "npx-direct", Transport: "stdio", Command: "npx", Args: []string{"-y", "demo-mcp", "--registry", "server-argument"}, Env: map[string]string{"MCP_TOKEN": "demo-token"}},
		{Name: "bunx-direct", Transport: "stdio", Command: "bunx", Args: []string{"demo-mcp"}},
		{Name: "env-override", Transport: "stdio", Command: "npx", Args: []string{"-y", "demo-mcp"}, Env: map[string]string{"npm_config_registry": "https://server.example/"}},
		{Name: "flag-override", Transport: "stdio", Command: "npx", Args: []string{"--package", "demo-mcp", "--registry=https://server.example/", "demo-mcp"}},
		{Name: "arbitrary", Transport: "stdio", Command: "node", Args: []string{"npx"}},
		{Name: "remote", Transport: "http", URL: "https://server.example/mcp"},
	}
	for _, s := range servers {
		s.Agents = []string{"codex"}
	}
	ok(t)(changeMCP(func(l *Library) error {
		l.MCP = append(l.MCP, servers...)
		return nil
	}))
	for _, registry := range []string{"https://upstream.example/npm", "https://another.example/npm", ""} {
		setMCPTestRegistry(t, registry)
		ok(t)(Sync())
		for _, s := range servers {
			e := codexEntry(t, cfg, s.Name)
			env, _ := e["env"].(map[string]any)
			for k, v := range s.Env {
				if got := env[k]; got != v {
					t.Errorf("%s: env %s = %v, want %s", s.Name, k, got, v)
				}
			}
			for _, k := range []string{"NPM_CONFIG_REGISTRY", "BUN_CONFIG_REGISTRY", "PNPM_CONFIG_REGISTRY", "YARN_REGISTRY", "YARN_NPM_REGISTRY_SERVER", "npm_config_registry"} {
				if _, ok := s.Env[k]; ok { // already checked above
					continue
				}
				if registry != "" && (s.Name == "npx-direct" || s.Name == "bunx-direct") && (k != "npm_config_registry" || runtime.GOOS != "windows") {
					if got := env[k]; got != registry+"/" {
						t.Errorf("%s: env %s = %v, want %s/", s.Name, k, got, registry)
					}
				} else if got, ok := env[k]; ok {
					t.Errorf("%s: unexpected env %s = %v", s.Name, k, got)
				}
			}
		}
		l, err := load()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range servers {
			if got := l.server(s.Name); !maps.Equal(got.Env, s.Env) {
				t.Errorf("%s: registry changed the Library's own env: %v", s.Name, got.Env)
			}
		}
	}
}

// TestFakeNPMInstall is run by a stand-in for npm, never by the real npm.
func TestFakeNPMInstall(t *testing.T) {
	if len(os.Args) < 4 || os.Args[2] != "--" || os.Args[3] != "install" {
		t.Skip("run by the MCP install test")
	}
	type capture struct {
		Registry, Bun, Other, Secret string
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(capture{os.Getenv("NPM_CONFIG_REGISTRY"), os.Getenv("BUN_CONFIG_REGISTRY"), os.Getenv("MAGPIE_FAKE_INSTALL_OTHER"), os.Getenv("MAGPIE_FAKE_MCP_SECRET")})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wd, "captured.json"), string(b))
	pkg := filepath.Join(wd, "node_modules", "demo-mcp")
	write(t, filepath.Join(pkg, "package.json"), `{"name":"demo-mcp","bin":"index.js"}`)
	write(t, filepath.Join(pkg, "index.js"), "#!/usr/bin/env node\n")
}

func TestMCPNpxInstallUsesRegistryAndSeparateCache(t *testing.T) {
	h := sandbox(t)
	npm := fakeManagerWithArgs(t, "npm", "-test.run=^TestFakeNPMInstall$ --")
	bin := filepath.Dir(npm)
	npx := filepath.Join(bin, "npx") + filepath.Ext(npm)
	node := filepath.Join(bin, "node")
	if runtime.GOOS == "windows" {
		node += ".exe"
	}
	write(t, node, "present")
	t.Setenv("NPM_CONFIG_REGISTRY", "https://inherited.example/")
	if runtime.GOOS != "windows" {
		t.Setenv("npm_config_registry", "https://lowercase.example/")
	}
	t.Setenv("MAGPIE_FAKE_INSTALL_OTHER", "kept")
	t.Setenv("MAGPIE_FAKE_MCP_SECRET", "")
	installed := make(chan string, 4)
	npxInstall = func(spec, cmd, dir, registry string) error {
		err := realInstall(spec, cmd, dir, registry)
		if err != nil {
			installed <- err.Error()
		}
		return err
	}
	npxSynced = func() { installed <- "" }
	t.Cleanup(func() { npxInstall, npxSynced = nil, nil })

	const spec = "demo-mcp@1.0.0"
	s := &Server{Name: "demo", Transport: "stdio", Command: npx, Args: []string{"-y", spec}, Env: map[string]string{"MAGPIE_FAKE_MCP_SECRET": "server-only"}, Agents: []string{"codex"}}
	// Even a pinned package in the old, unconfigured cache cannot stand
	// in for an install through the explicit upstream.
	old := filepath.Join(npxDir(spec, ""), "node_modules", "demo-mcp")
	write(t, filepath.Join(old, "package.json"), `{"bin":"index.js"}`)
	write(t, filepath.Join(old, "index.js"), "old package")
	first, second := "https://first.example/npm", "https://second.example/npm"
	setMCPTestRegistry(t, first)
	ok(t)(SaveServer("", *s))
	cfg := filepath.Join(h, ".codex", "config.toml")
	await := func() {
		t.Helper()
		select {
		case err := <-installed:
			if err != "" {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("fake installer didn't finish")
		}
	}
	verify := func(registry string) string {
		t.Helper()
		await()
		ok(t)(SyncMCP())
		e := codexEntry(t, cfg, "demo")
		args, _ := e["args"].([]any)
		if e["command"] != nodeBeside(npx) || len(args) != 1 {
			t.Fatalf("agent didn't get optimized command: %v", e)
		}
		bin := args[0].(string)
		dir := filepath.Dir(filepath.Dir(filepath.Dir(bin)))
		var got struct{ Registry, Bun, Other, Secret string }
		if err := json.Unmarshal([]byte(read(t, filepath.Join(dir, "captured.json"))), &got); err != nil {
			t.Fatal(err)
		}
		if got.Registry != registry+"/" || got.Bun != registry+"/" || got.Other != "kept" || got.Secret != "" {
			t.Fatalf("installer child env = %+v for %s", got, registry)
		}
		if env, _ := e["env"].(map[string]any); len(env) != 1 || env["MAGPIE_FAKE_MCP_SECRET"] != "server-only" {
			t.Errorf("optimized server did not keep its own env: %v", env)
		}
		return bin
	}
	a := verify(first)
	setMCPTestRegistry(t, second)
	ok(t)(SyncMCP())
	b := verify(second)
	if a == b {
		t.Fatal("the optimized command reused a package installed using another upstream")
	}
	setMCPTestRegistry(t, "")
	ok(t)(SyncMCP())
	e := codexEntry(t, cfg, "demo")
	if args, _ := e["args"].([]any); len(args) != 1 || args[0] != filepath.Join(old, "index.js") {
		t.Fatalf("clearing the registry did not return to the original unconfigured cache: %v", e)
	}

	// A user registry option, by env or flag, never queues a separate
	// background npm even with an otherwise optimizable command.
	for _, override := range []*Server{
		{Transport: "stdio", Command: npx, Args: []string{"-y", "override-env"}, Env: map[string]string{"nPm_cOnFiG_ReGiStRy": "https://server.example/"}},
		{Transport: "stdio", Command: npx, Args: []string{"-y", "--registry", "https://server.example/", "override-flag"}},
	} {
		if _, ok := readNpx(override); ok || npxDirect(override, second) != override {
			t.Errorf("server with a registry override was eligible for background install: %+v", override)
		}
	}
}

// fakeManager makes a package-manager shim that runs the test binary, so
// health and Codex can exercise their real process environment without npm.
func fakeManager(t *testing.T, name string) string {
	t.Helper()
	return fakeManagerWithArgs(t, name, "")
}

func fakeManagerWithArgs(t *testing.T, name, fixedArgs string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		path += ".cmd"
		write(t, path, "@echo off\r\n\""+self+"\" "+fixedArgs+" %*\r\n")
	} else {
		testenv.Program(t, path, "#!/bin/sh\nexec '"+strings.ReplaceAll(self, "'", "'\\''")+"' "+fixedArgs+" \"$@\"\n")
	}
	return path
}

func captureMCPNPMEnv(t *testing.T, path string) {
	t.Helper()
	b, err := json.Marshal([]string{os.Getenv("NPM_CONFIG_REGISTRY"), os.Getenv("BUN_CONFIG_REGISTRY"), os.Getenv("npm_config_registry")})
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(b))
}

func TestMCPCheckNPMRegistryRechecksWithNewUpstream(t *testing.T) {
	sandbox(t)
	const first = "https://check-first.example/npm"
	const second = "https://check-second.example/npm"
	t.Setenv("NPM_CONFIG_REGISTRY", "https://inherited.example/")
	if runtime.GOOS != "windows" {
		t.Setenv("npm_config_registry", "https://lowercase.example/")
	}
	setMCPTestRegistry(t, first)
	path := filepath.Join(t.TempDir(), "env.json")
	s := fakeStdio(t, "ok", map[string]string{"MAGPIE_FAKE_MCP_NPM_ENV": path})
	s.Name, s.Command = "npm-check", fakeManager(t, "npx")
	ok(t)(SaveServer("", *s))
	for _, registry := range []string{first, second, ""} {
		setMCPTestRegistry(t, registry)
		h, err := CheckServers(t.Context(), []string{s.Name}, false)
		if err != nil || h[s.Name].State != "ok" {
			t.Fatalf("health check failed: %+v %v", h, err)
		}
		var got []string
		if err := json.Unmarshal([]byte(read(t, path)), &got); err != nil {
			t.Fatal(err)
		}
		want := []string{registry + "/", registry + "/", ""}
		if registry == "" {
			want = []string{"https://inherited.example/", os.Getenv("BUN_CONFIG_REGISTRY"), os.Getenv("npm_config_registry")}
		}
		if runtime.GOOS == "windows" {
			want[2] = want[0] // environment naming is case-insensitive
		}
		if !slices.Equal(got, want) {
			t.Errorf("health check child env = %v, want %v", got, want)
		}
	}
}

func TestMCPRunNPMRegistryFollowsExternalSave(t *testing.T) {
	sandbox(t)
	const first, second = "https://before.example/npm", "https://after.example/npm"
	setMCPTestRegistry(t, first)
	s := &Server{Name: "npm-run", Transport: "stdio", Command: "bunx", Args: []string{"demo-mcp"}}
	spec, ok := runPackageSpec(s, first)
	if !ok || spec.Registry != first {
		t.Fatalf("initial spec: %+v", spec)
	}
	resetRuns(t)
	runGiven(map[string]runSpec{s.Name: spec})
	for _, upstream := range []string{second, ""} {
		// No sync here: the Settings CLI can be in another process.
		setMCPTestRegistry(t, upstream)
		got, ok := runSpecOf(s.Name)
		if !ok || got.Registry != upstream {
			t.Fatalf("after saving %q, running gateway kept %q", upstream, got.Registry)
		}
	}
}

func TestMCPRunNPMRegistryUsesSyncedUpstream(t *testing.T) {
	sandbox(t)
	const upstream = "https://codex.example/npm"
	t.Setenv("NPM_CONFIG_REGISTRY", "https://inherited.example/")
	if runtime.GOOS != "windows" {
		t.Setenv("npm_config_registry", "https://lowercase.example/")
	}
	setMCPTestRegistry(t, upstream)
	dir := t.TempDir()
	path := filepath.Join(dir, "env.json")
	s := &Server{
		Name: "npm-run", Transport: "stdio", Command: fakeManager(t, "pnpm"), Args: []string{"-test.run=^TestFakeRunServer$"},
		Env: map[string]string{"MAGPIE_FAKE_RUN": filepath.Join(dir, "log"), "MAGPIE_FAKE_MCP_NPM_ENV": path},
	}
	spec, ok := specOf(s)
	if !ok {
		t.Fatal("server was not given as a command")
	}
	setMCPTestRegistry(t, "https://changed-after-sync.example/npm")
	c, _, err := runStart(&runSession{name: s.Name, spec: spec}, spec, defaultInit())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c.end()
		<-c.done
	}()
	var got []string
	if err := json.Unmarshal([]byte(read(t, path)), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{upstream + "/", upstream + "/", ""}
	if runtime.GOOS == "windows" {
		want[2] = want[0]
	}
	if !slices.Equal(got, want) {
		t.Errorf("Codex child env = %v, want the synced snapshot %v", got, want)
	}
}
