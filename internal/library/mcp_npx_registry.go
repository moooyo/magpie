package library

import (
	"maps"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/npmregistry"
)

// npmEnvOverride detects a default registry or alternate config file given
// to this server. Scope-specific settings still override the installed
// default; the package manager matches them to its own package names.
func npmEnvOverride(env map[string]string) bool {
	for k := range env {
		if npmregistry.IsVar(k) {
			return true
		}
		switch strings.ToLower(k) {
		case "npm_config_userconfig", "npm_config_globalconfig", "yarn_rc_filename":
			return true
		}
	}
	return false
}

// npmInstallEnv reports any package-manager configuration that the
// separate installer would miss, including scoped auth and overrides.
func npmInstallEnv(env map[string]string) bool {
	if npmEnvOverride(env) {
		return true
	}
	for k := range env {
		if strings.HasPrefix(strings.ToLower(k), "npm_config_") {
			return true
		}
		if strings.EqualFold(k, "NPM_TOKEN") || strings.EqualFold(k, "NODE_AUTH_TOKEN") {
			return true
		}
	}
	return false
}

// npmRegistryFor is the default to give a direct package manager, if any.
// Nothing is inferred about what an arbitrary command starts.
func npmRegistryFor(registry, command string, args []string, env map[string]string) string {
	if registry == "" || !npmregistry.Program(command) || npmEnvOverride(env) {
		return ""
	}
	// These launchers pass everything after the package or command to
	// the server. Its own --config or --registry is not an npm override.
	program := progName(command)
	launcher := program == "npx" || program == "bunx" || program == "pnpx"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			if launcher {
				break
			}
			launcher = arg == "dlx" || program == "bun" && arg == "x"
			continue
		}
		flag, _, inline := strings.Cut(arg, "=")
		switch strings.TrimLeft(strings.ToLower(flag), "-") {
		case "registry", "npm-registry-server", "config.registry", "userconfig", "globalconfig", "config", "rc-file", "bunfig":
			return ""
		case "p", "package", "c", "call", "cache", "prefix", "shell", "script-shell", "loglevel":
			if !inline && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
		}
	}
	return registry
}

// mcpRegistry is the upstream that applies to a direct package manager
// in the Library. Program recognizes the basename even when the command's
// directory is an agent variable rather than a fixed path.
func mcpRegistry(s *Server, registry string) string {
	if registry == "" || s.Transport != "stdio" {
		return ""
	}
	return npmRegistryFor(registry, s.Command, s.Args, s.Env)
}

// hostMCPRegistry checks the actual command Magpie would start, including
// a program selected by a variable in the host environment.
func hostMCPRegistry(s *Server, registry string) string {
	if registry == "" || s.Transport != "stdio" {
		return ""
	}
	command, args, env := launchCmd(s, "")
	return npmRegistryFor(registry, command, args, env)
}

// withNPMRegistry is the server's launch configuration, without changing
// the Library definition. Agent files can add to an inherited environment
// but cannot remove variables from it, so replace npm's lowercase alias too
// on systems where the two names are distinct.
func withNPMRegistry(s *Server, registry string, wsl bool) *Server {
	if wsl {
		registry = mcpRegistry(s, registry)
	} else {
		registry = hostMCPRegistry(s, registry)
	}
	if registry == "" {
		return s
	}
	c := *s
	c.Env = maps.Clone(s.Env)
	if c.Env == nil {
		c.Env = map[string]string{}
	}
	for _, kv := range npmRegistryVars(registry, wsl || runtime.GOOS != "windows") {
		k, v, _ := strings.Cut(kv, "=")
		c.Env[k] = v
	}
	return &c
}

// withMCPEnv overlays the server's variables. On Unix, merely appending
// NPM_CONFIG_REGISTRY would leave an inherited npm_config_registry present.
func withMCPEnv(env []string, own map[string]string, packageManager bool) []string {
	if packageManager && runtime.GOOS != "windows" {
		for k := range own {
			if npmregistry.IsVar(k) {
				env = slices.DeleteFunc(env, func(kv string) bool {
					key, _, _ := strings.Cut(kv, "=")
					return strings.EqualFold(key, k)
				})
			}
		}
	}
	for k, v := range own {
		env = append(env, k+"="+v)
	}
	return env
}

func npmRegistryVars(registry string, caseSensitive bool) []string {
	vars := npmregistry.Vars(registry)
	if caseSensitive {
		for _, kv := range vars {
			if v, ok := strings.CutPrefix(kv, "NPM_CONFIG_REGISTRY="); ok {
				return append(vars, "npm_config_registry="+v)
			}
		}
	}
	return vars
}
