package plan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jgsqware/pine/internal/model"
	"github.com/jgsqware/pine/internal/scanner"
)

// a non-decryptable but well-formed vault blob — enough to exercise detection
// and masking without needing ansible-vault.
const sampleVaultBlob = "$ANSIBLE_VAULT;1.1;AES256\n" +
	"6532656232646666306265656636393933643333646236353634323235643235\n" +
	"3261633433326336313362623132623932316131376164320a336131316335\n"

func writeRepo(t *testing.T, files map[string]string) (*model.ScanResult, string) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := scanner.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	return res, root
}

func firstTaskArgs(out *Result) string {
	for _, pp := range out.Plays {
		for _, tp := range pp.Tasks {
			return tp.Args
		}
	}
	return ""
}

// A vars_prompt variable resolves to its default; a caller-supplied answer
// (Request.Vars) overrides it; and both feed nested {{ }} expansion.
func TestPlanResolvesVarsPrompt(t *testing.T) {
	res, root := writeRepo(t, map[string]string{
		"inv/hosts":              "[web]\nweb01\n",
		"inv/group_vars/all.yml": "registry: reg.local\n",
		"site.yml": "- hosts: all\n" +
			"  vars_prompt:\n" +
			"    - name: image\n      prompt: image?\n      default: app:1.0\n" +
			"  tasks:\n" +
			"    - name: pull\n      ansible.builtin.command: \"pull {{ registry }}/{{ image }}\"\n",
	})
	repo := model.Repo{ID: "r", Name: "vt"}

	out, err := Compute(res, root, repo, Request{Playbook: "site.yml", Inventory: "hosts"})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstTaskArgs(out); got != "pull reg.local/app:1.0" {
		t.Errorf("prompt default not resolved: %q", got)
	}
	// a provided answer overrides the default
	out, _ = Compute(res, root, repo, Request{Playbook: "site.yml", Inventory: "hosts", Vars: map[string]any{"image": "app:2.0"}})
	if got := firstTaskArgs(out); got != "pull reg.local/app:2.0" {
		t.Errorf("prompt answer not applied: %q", got)
	}
}

// Vault-encrypted vars are detected and, without a password, masked so the raw
// blob never leaks into a resolved task argument.
func TestPlanVaultMaskedWithoutPassword(t *testing.T) {
	res, root := writeRepo(t, map[string]string{
		"inv/hosts": "[web]\nweb01\n",
		"inv/group_vars/all.yml": "db_password: !vault |\n" +
			indentBlock(sampleVaultBlob),
		"site.yml": "- hosts: all\n  tasks:\n" +
			"    - name: use\n      ansible.builtin.command: \"login --pass {{ db_password }}\"\n",
	})
	out, err := Compute(res, root, model.Repo{ID: "r", Name: "vt"}, Request{Playbook: "site.yml", Inventory: "hosts"})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out.Summary.VaultVars, "db_password") {
		t.Errorf("db_password not flagged as vault: %v", out.Summary.VaultVars)
	}
	if got := firstTaskArgs(out); got != "login --pass "+vaultMask {
		t.Errorf("vault value not masked: %q", got)
	}
}

// With the right password, a vault value decrypts and resolves. Gated on
// ansible-vault being installed.
func TestPlanVaultDecrypts(t *testing.T) {
	if _, err := exec.LookPath("ansible-vault"); err != nil {
		t.Skip("ansible-vault not installed")
	}
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "pw")
	if err := os.WriteFile(pwFile, []byte("s3cr3t"), 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := exec.Command("ansible-vault", "encrypt_string", "--vault-password-file", pwFile,
		"topsecret", "--name", "db_password").Output()
	if err != nil {
		t.Fatalf("encrypt_string: %v", err)
	}
	res, root := writeRepo(t, map[string]string{
		"inv/hosts":              "[web]\nweb01\n",
		"inv/group_vars/all.yml": string(enc) + "\n",
		"site.yml": "- hosts: all\n  tasks:\n" +
			"    - name: use\n      ansible.builtin.command: \"login --pass {{ db_password }}\"\n",
	})
	out, err := Compute(res, root, model.Repo{ID: "r", Name: "vt"},
		Request{Playbook: "site.yml", Inventory: "hosts", VaultPassword: "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	// The engine decrypts internally, but the exposed plan must redact the
	// secret: the plaintext must never reach the resolved arg.
	if got := firstTaskArgs(out); got != "login --pass "+RedactedMark {
		t.Errorf("decrypted vault value not redacted in args: %q", got)
	}
	if strings.Contains(planText(out), "topsecret") {
		t.Errorf("decrypted secret leaked into plan: %s", planText(out))
	}
}

// A secret-looking plaintext var (the exact shape a decrypted vault value takes
// once injected into eff) must be redacted in both the task name and args, and
// never appear in clear anywhere in the returned plan. Deterministic: needs no
// ansible-vault.
func TestPlanRedactsResolvedSecret(t *testing.T) {
	res, root := writeRepo(t, map[string]string{
		"inv/hosts":              "[web]\nweb01\n",
		"inv/group_vars/all.yml": "db_password: topsecret\nserver_tokens: \"off\"\n",
		"site.yml": "- hosts: all\n  tasks:\n" +
			"    - name: \"login {{ server_tokens }} {{ db_password }}\"\n" +
			"      ansible.builtin.command: \"login --pass {{ db_password }}\"\n",
	})
	out, err := Compute(res, root, model.Repo{ID: "r", Name: "vt"}, Request{Playbook: "site.yml", Inventory: "hosts"})
	if err != nil {
		t.Fatal(err)
	}
	tp := out.Plays[0].Tasks[0]
	// server_tokens: "off" is a toggle, not a secret — it stays visible.
	if tp.Name != "login off "+RedactedMark {
		t.Errorf("name not redacted as expected: %q", tp.Name)
	}
	if tp.Args != "login --pass "+RedactedMark {
		t.Errorf("args not redacted as expected: %q", tp.Args)
	}
	if strings.Contains(planText(out), "topsecret") {
		t.Errorf("secret leaked into plan: %s", planText(out))
	}
}

// redactSecretVars masks vault-derived and secret-looking values while leaving
// toggles/numeric knobs and undecryptable blobs untouched.
func TestRedactSecretVars(t *testing.T) {
	vars := map[string]any{
		"db_password":   "topsecret", // decrypted secret (plaintext)
		"registry":      "reg.local", // ordinary value
		"server_tokens": "off",       // toggle under a secret-ish name
		"http_port":     8080,        // numeric knob
		"legacy_blob":   vaultMask,   // undecryptable blob, already masked
		"opaque":        "42",        // non-secret key holding vault plaintext
	}
	// "opaque" carries a decrypted vault value even though its name is innocuous.
	got := redactSecretVars(vars, map[string]bool{"db_password": true, "opaque": true})
	want := map[string]any{
		"db_password":   RedactedMark,
		"registry":      "reg.local",
		"server_tokens": "off",
		"http_port":     8080,
		"legacy_blob":   vaultMask,
		"opaque":        RedactedMark,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("redactSecretVars[%q] = %v, want %v", k, got[k], w)
		}
	}
}

// planText serializes a plan's user-facing name/arg fields so a test can assert
// no secret substring leaked anywhere.
func planText(out *Result) string {
	var b strings.Builder
	for _, pp := range out.Plays {
		for _, tp := range pp.Tasks {
			b.WriteString(tp.Name)
			b.WriteByte('\n')
			b.WriteString(tp.Args)
			b.WriteByte('\n')
			b.WriteString(tp.RawName)
			b.WriteByte('\n')
			b.WriteString(tp.RawArgs)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// indentBlock indents each line of a YAML block scalar by ten spaces.
func indentBlock(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("          " + line + "\n")
	}
	return b.String()
}
