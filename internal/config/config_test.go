package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geraldcsoftware/itop-cli/internal/config"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRejectsUnsafeConfig(t *testing.T) {
	cases := map[string]string{
		"password key":      "url = \"https://itop.example.internal\"\nusername = \"jdoe\"\npassword = \"x\"\n",
		"literal secret":    "url = \"https://itop.example.internal\"\nusername = \"jdoe\"\ncredential = \"hunter2\"\n",
		"cleartext url":     "url = \"http://itop.example.internal\"\nusername = \"jdoe\"\ncredential = \"env:ITOP_PW\"\n",
		"literal bws token": "url = \"https://itop.example.internal\"\nusername = \"jdoe\"\ncredential = \"env:ITOP_PW\"\n[bws]\naccessToken = \"hunter2\"\n",
		"unknown auth":      "url = \"https://itop.example.internal\"\nusername = \"jdoe\"\ncredential = \"env:ITOP_PW\"\nauth = \"basic\"\n",
	}
	for name, body := range cases {
		_, err := config.Load(writeTemp(t, body))
		if err == nil {
			t.Errorf("%s: Load accepted unsafe config", name)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: error repeats the secret: %v", name, err)
		}
	}
	cfg, err := config.Load(writeTemp(t, "url = \"https://itop.example.internal\"\nusername = \"jdoe\"\ncredential = \"keychain:itop-cli/jdoe\"\n"))
	if err != nil || cfg.Auth != "password" || cfg.APIVersion != "1.3" || cfg.Timeout != 30*time.Second {
		t.Fatalf("defaults not applied: %+v, %v", cfg, err)
	}
}
