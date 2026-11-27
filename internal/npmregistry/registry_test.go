package npmregistry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

func TestExplicitRegistryKeepsPathAndCredentials(t *testing.T) {
	home := t.TempDir()
	testenv.SetHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("REGISTRY_TEST_TOKEN", "only-for-upstream")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/outside" {
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("sent auth outside the registry path: %q", got)
			}
			return
		}
		if r.URL.RequestURI() != "/repo/@scope%2Fexample/latest?tag=x%2Fy" {
			t.Errorf("wrapped URI = %s", r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer only-for-upstream" {
			t.Errorf("auth in registry = %q", got)
		}
		http.Redirect(w, r, "/outside", http.StatusFound)
	}))
	t.Cleanup(s.Close)
	if err := os.WriteFile(filepath.Join(home, ".npmrc"), []byte(fmt.Sprintf("@scope:registry=%s/repo/\n//%s/repo/:_authToken=${REGISTRY_TEST_TOKEN}\n", s.URL, strings.TrimPrefix(s.URL, "http://"))), 0o600); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, Default+"/@scope%2Fexample/latest?tag=x%2Fy", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Do(s.Client(), req, "https://unused.invalid", "@scope/example")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Request.URL.Path != "/outside" {
		t.Fatalf("redirect went to %s", res.Request.URL)
	}
}

func TestExplicitRegistryRefusesPublicRedirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, Default+"/example/latest", http.StatusFound)
	}))
	t.Cleanup(s.Close)
	client := s.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		t.Error("the public redirect reached the client's redirect hook")
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequest(http.MethodGet, Default+"/example/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Do(client, req, s.URL, "")
	if err == nil || !strings.Contains(err.Error(), "redirected to npm's public service") {
		if res != nil {
			res.Body.Close()
		}
		t.Fatalf("public redirect wasn't refused: %v", err)
	}
}

func TestExplicitRegistryReplacesOnlyRegistryEnvironment(t *testing.T) {
	old := []string{"PATH=/here", "Npm_Config_Registry=https://old.invalid", "npm_config_registry=https://other.invalid", "BUN_CONFIG_REGISTRY=https://old.invalid", "pnpm_config_registry=https://old.invalid", "NPM_TOKEN=keep"}
	const upstream = "https://other.invalid/repo"
	got := WithEnv(old, upstream)
	want := []string{"PATH=/here", "NPM_TOKEN=keep", "BUN_CONFIG_REGISTRY=" + upstream + "/", "NPM_CONFIG_REGISTRY=" + upstream + "/", "PNPM_CONFIG_REGISTRY=" + upstream + "/", "YARN_REGISTRY=" + upstream + "/", "YARN_NPM_REGISTRY_SERVER=" + upstream + "/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child env = %v, want %v", got, want)
	}
	if old[1] != "Npm_Config_Registry=https://old.invalid" || !reflect.DeepEqual(WithEnv(old, ""), old) {
		t.Fatal("the original or automatic environment was changed")
	}
}
