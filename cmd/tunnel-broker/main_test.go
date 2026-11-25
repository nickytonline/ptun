package main

import (
	"reflect"
	"testing"
)

func setAll(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"SHARED_SECRET", "CONFIG_API_URLS", "BASE_DOMAIN", "SSH_HOST", "BROKER_HOST", "JWKS_URL", "JWT_AUDIENCE", "DEV_AUTH_EMAIL"} {
		t.Setenv(k, kv[k])
	}
}

func TestMissingRequired(t *testing.T) {
	full := map[string]string{
		"SHARED_SECRET":   "c2VjcmV0",
		"CONFIG_API_URLS": "https://pomerium.internal:5443",
		"BASE_DOMAIN":     "example.com",
		"SSH_HOST":        "ssh.example.com",
		"BROKER_HOST":     "tunnels.example.com",
	}
	with := func(changes map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range full {
			m[k] = v
		}
		for k, v := range changes {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"all set", full, nil},
		{"nothing set", map[string]string{}, []string{"SHARED_SECRET", "CONFIG_API_URLS", "BASE_DOMAIN", "SSH_HOST", "BROKER_HOST"}},
		{"whitespace counts as unset", with(map[string]string{"BASE_DOMAIN": "  "}), []string{"BASE_DOMAIN"}},
		{"broker host needed for JWT defaults", with(map[string]string{"BROKER_HOST": ""}), []string{"BROKER_HOST"}},
		{"broker host needed if only JWKS_URL is set", with(map[string]string{"BROKER_HOST": "", "JWKS_URL": "https://t.example.com/jwks.json"}), []string{"BROKER_HOST"}},
		{"broker host optional with JWKS_URL and JWT_AUDIENCE", with(map[string]string{"BROKER_HOST": "", "JWKS_URL": "https://t.example.com/jwks.json", "JWT_AUDIENCE": "t.example.com"}), nil},
		{"no URLs in CONFIG_API_URLS", with(map[string]string{"CONFIG_API_URLS": " , "}), []string{"CONFIG_API_URLS"}},
		{"blank entries in CONFIG_API_URLS are fine", with(map[string]string{"CONFIG_API_URLS": "https://a:5443, ,https://b:5443,"}), nil},
		{"whitespace JWT_AUDIENCE still needs broker host", with(map[string]string{"BROKER_HOST": "", "JWKS_URL": "https://t.example.com/jwks.json", "JWT_AUDIENCE": " "}), []string{"BROKER_HOST"}},
		{"whitespace DEV_AUTH_EMAIL is not dev mode", with(map[string]string{"BROKER_HOST": "", "DEV_AUTH_EMAIL": " "}), []string{"BROKER_HOST"}},
		{"broker host optional in dev mode", with(map[string]string{"BROKER_HOST": "", "DEV_AUTH_EMAIL": "you@example.com"}), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAll(t, tt.env)
			if got := missingRequired(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingRequired() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSettingsAreTrimmed(t *testing.T) {
	t.Setenv("BASE_DOMAIN", " example.com\n")
	if got := env("BASE_DOMAIN", ""); got != "example.com" {
		t.Errorf("env(BASE_DOMAIN) = %q, want %q", got, "example.com")
	}
	t.Setenv("CONFIG_API_URLS", " https://a:5443 ,, https://b:5443 ")
	if got, want := configAPIURLs(), []string{"https://a:5443", "https://b:5443"}; !reflect.DeepEqual(got, want) {
		t.Errorf("configAPIURLs() = %q, want %q", got, want)
	}
}
