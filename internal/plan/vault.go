package plan

import (
	"os"
	"os/exec"
	"strings"

	"github.com/jgsqware/pine/internal/ansible"
)

// vaultMask replaces a vault value that could not be decrypted, so the raw
// $ANSIBLE_VAULT blob never appears in a resolved task name or argument.
const vaultMask = "***vault***"

// vaultDecryptor decrypts inline ansible-vault scalars with a password via the
// ansible-vault CLI. Plaintext is cached by blob so the same secret repeated
// across hosts costs a single subprocess. The password lives only for the
// decryptor's lifetime (a temp password file removed on close).
type vaultDecryptor struct {
	pwFile string
	cache  map[string]string
}

// newVaultDecryptor builds a decryptor for password. It returns (nil, cleanup,
// nil) when no password is given, and (nil, cleanup, err) when ansible-vault is
// unavailable or the temp password file can't be written — callers surface err
// as a non-fatal note (the plan still renders, vault values just stay masked).
func newVaultDecryptor(password string) (*vaultDecryptor, func(), error) {
	noop := func() {}
	if strings.TrimSpace(password) == "" {
		return nil, noop, nil
	}
	if !ansible.Available("ansible-vault") {
		return nil, noop, exec.ErrNotFound
	}
	f, err := os.CreateTemp("", "pine-vault-pw-*")
	if err != nil {
		return nil, noop, err
	}
	if _, err := f.WriteString(password); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, noop, err
	}
	f.Close()
	d := &vaultDecryptor{pwFile: f.Name(), cache: map[string]string{}}
	return d, func() { os.Remove(f.Name()) }, nil
}

// decrypt returns the plaintext for one vault blob, or ("", false) on failure.
func (d *vaultDecryptor) decrypt(blob string) (string, bool) {
	if d == nil {
		return "", false
	}
	if v, ok := d.cache[blob]; ok {
		return v, true
	}
	tmp, err := os.CreateTemp("", "pine-vault-*")
	if err != nil {
		return "", false
	}
	defer os.Remove(tmp.Name())
	// the blob may be stored with escaped "\n" or wrapped oddly; normalize to a
	// real multi-line vault file with a trailing newline.
	body := strings.ReplaceAll(strings.TrimSpace(blob), "\\n", "\n")
	if _, err := tmp.WriteString(body + "\n"); err != nil {
		tmp.Close()
		return "", false
	}
	tmp.Close()
	vc := exec.Command(ansible.Bin("ansible-vault"), "decrypt", tmp.Name(),
		"--output", "-", "--vault-password-file", d.pwFile)
	vc.Env = ansible.Env()
	out, err := vc.Output()
	if err != nil {
		return "", false
	}
	res := strings.TrimRight(string(out), "\n")
	d.cache[blob] = res
	return res, true
}

// decryptVaultVars replaces vault-encrypted scalar values in eff with their
// plaintext (best effort: a blob that fails to decrypt is left in place).
func decryptVaultVars(eff map[string]any, d *vaultDecryptor) {
	if d == nil {
		return
	}
	for k, v := range eff {
		if s, ok := v.(string); ok && isVaultValue(s) {
			if plain, ok := d.decrypt(s); ok {
				eff[k] = plain
			}
		}
	}
}
