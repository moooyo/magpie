package library

import (
	"maps"
	"path/filepath"
	"testing"
)

func TestProjectMCPRegistryFollowsSettings(t *testing.T) {
	sandbox(t)
	proj := t.TempDir()
	path := filepath.Join(proj, ".mcp.json")
	const own = `{"command":"npx","args":["team-server"],"env":{"NPM_CONFIG_REGISTRY":"https://team.example/"}}`
	write(t, path, `{"mcpServers":{"team":`+own+`}}`)
	entries := func() map[string]*Server {
		t.Helper()
		got, err := (&mcpFile{Path: path, Format: fmtClaude}).read()
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	team := entries()["team"]
	if team == nil {
		t.Fatal("project fixture has no team server")
	}
	ok(t)(AddProject(proj))
	a := Server{Name: "defaulted", Transport: "stdio", Command: "pnpm", Args: []string{"dlx", "demo-mcp"}, Env: map[string]string{"MCP_TOKEN": "test-only"}}
	b := Server{Name: "overridden", Transport: "stdio", Command: "npx", Args: []string{"-y", "demo-mcp"}, Env: map[string]string{"NPM_CONFIG_REGISTRY": "https://server.example/"}}
	for _, s := range []Server{a, b} {
		ok(t)(SaveServer("", s))
		ok(t)(ProjectServer(proj, s.Name, []string{"claude"}))
	}
	for _, registry := range []string{"https://first.example/npm", "https://second.example/npm", ""} {
		setMCPTestRegistry(t, registry)
		ok(t)(SyncMCP())
		got := entries()
		if got["team"] == nil || !got["team"].same(team) || got[b.Name] == nil || !got[b.Name].same(&b) {
			t.Fatalf("registry %q changed the project's own server or an override: %v", registry, got)
		}
		if got[a.Name] == nil {
			t.Fatalf("registry %q removed the managed project server", registry)
		}
		env := got[a.Name].Env
		if registry != "" {
			if env["NPM_CONFIG_REGISTRY"] != registry+"/" || env["MCP_TOKEN"] != a.Env["MCP_TOKEN"] {
				t.Errorf("project registry %q: env = %v", registry, env)
			}
		} else if !maps.Equal(env, a.Env) {
			t.Errorf("clearing registry retained an overlay in the project: %v", env)
		}
	}
	l, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if s := l.server(a.Name); s == nil || !s.same(&a) {
		t.Errorf("project sync changed the Library definition: %+v", s)
	}
}
