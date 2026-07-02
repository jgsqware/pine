package plan

import "testing"

func TestIsSecretKey(t *testing.T) {
	secret := []string{"db_password", "api_key", "vault_pg_pass", "app_secret", "access_key", "gpg_passphrase", "vault_anything"}
	for _, k := range secret {
		if !IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = false, want true", k)
		}
	}
	notSecret := []string{"app_version", "server_tokens", "nginx_workers", "http_port", "replica_count"}
	for _, k := range notSecret {
		if IsSecretKey(k) {
			t.Errorf("IsSecretKey(%q) = true, want false", k)
		}
	}
}
