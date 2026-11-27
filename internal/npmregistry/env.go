package npmregistry

import (
	"os"
	"path"
	"strings"
)

// Program recognizes direct package manager commands, including Windows shims.
// Nothing is inferred about arbitrary commands or shell scripts.
func Program(command string) bool {
	name := path.Base(strings.ToLower(strings.ReplaceAll(command, `\`, "/")))
	switch ext := path.Ext(name); ext {
	case ".exe", ".cmd", ".bat", ".ps1", ".com":
		name = strings.TrimSuffix(name, ext)
	}
	switch name {
	case "npm", "npm-cli.js", "npx", "npx-cli.js", "pnpm", "pnpx", "bun", "bunx", "yarn", "yarnpkg":
		return true
	}
	return false
}

// IsVar reports whether a variable supplies a package manager's default registry.
func IsVar(key string) bool {
	switch strings.ToUpper(key) {
	case "BUN_CONFIG_REGISTRY", "NPM_CONFIG_REGISTRY", "PNPM_CONFIG_REGISTRY", "YARN_REGISTRY", "YARN_NPM_REGISTRY_SERVER":
		return true
	}
	return false
}

// Vars are the registry assignments understood by Magpie's package managers.
func Vars(registry string) []string {
	if registry == "" {
		return nil
	}
	u := strings.TrimRight(registry, "/") + "/"
	return []string{"BUN_CONFIG_REGISTRY=" + u, "NPM_CONFIG_REGISTRY=" + u, "PNPM_CONFIG_REGISTRY=" + u, "YARN_REGISTRY=" + u, "YARN_NPM_REGISTRY_SERVER=" + u}
}

// WithEnv sets the upstream in one child, leaving the process's environment
// and npmrc alone. Remove both spellings of npm_config_registry: npm gives
// the lowercase form preference in scripts, and Windows ignores case.
func WithEnv(env []string, registry string) []string {
	vars := Vars(registry)
	if len(vars) == 0 {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+len(vars))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !IsVar(k) {
			out = append(out, kv)
		}
	}
	return append(out, vars...)
}

// Env applies the configured upstream to one managed child.
func Env(env []string) []string { return WithEnv(env, Configured()) }
