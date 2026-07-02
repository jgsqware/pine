package runner

import "testing"

func TestValidateGitURL(t *testing.T) {
	ok := []string{
		"https://github.com/acme/infra.git",
		"http://gitlab.local/acme/infra",
		"git://example.com/repo.git",
		"ssh://git@example.com/acme/infra.git",
		"git@github.com:acme/infra.git", // scp-like implicit ssh
	}
	for _, u := range ok {
		if err := ValidateGitURL(u); err != nil {
			t.Errorf("ValidateGitURL(%q) = %v, want nil", u, err)
		}
	}
	bad := []string{
		"",
		"ext::sh -c 'touch /tmp/pwned'", // transport helper → RCE
		"fd::17/foo",
		"file:///etc/passwd",
		"ftp://example.com/repo",
		"transport::whatever",
	}
	for _, u := range bad {
		if err := ValidateGitURL(u); err == nil {
			t.Errorf("ValidateGitURL(%q) = nil, want error", u)
		}
	}
}
