//go:build integration && !noonebot && !noirc

package e2e

import (
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Each test owns its server, listener and database. Start from the binary's
// defaults so the fixture does not duplicate Ergo's full configuration.
func startErgo(t *testing.T, casemapping string, configure ...func(map[string]any)) string {
	t.Helper()
	binary := requiredEnv(t, "E2E_ERGO")
	data, err := exec.Command(binary, "defaultconfig").Output()
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	dir, address := t.TempDir(), freeAddress(t)
	server := cfg["server"].(map[string]any)
	server["name"] = "ergo.test"
	server["listeners"] = map[string]any{address: map[string]any{}}
	server["casemapping"] = casemapping
	server["lookup-hostnames"] = false
	server["check-ident"] = false
	server["motd"] = filepath.Join(dir, "ergo.motd")
	cfg["datastore"].(map[string]any)["path"] = filepath.Join(dir, "ircd.db")
	cfg["lock-file"] = filepath.Join(dir, "ircd.lock")
	cfg["fakelag"].(map[string]any)["enabled"] = false
	cfg["languages"].(map[string]any)["enabled"] = false
	cfg["roleplay"].(map[string]any)["enabled"] = true
	cfg["allow-environment-overrides"] = false
	for _, modify := range configure {
		modify(cfg)
	}
	data, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ircd.yaml")
	writeFile(t, path, data)
	writeFile(t, server["motd"].(string), []byte("matterbridge integration test\n"))
	start(t, dir, "ergo", binary, "run", "--conf", path)
	return address
}
