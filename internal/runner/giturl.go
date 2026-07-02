package runner

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// allowedGitSchemes are the transport schemes Pine will clone from. Notably
// absent: git's "transport helper" syntax (ext::, fd::) which can execute an
// arbitrary shell command — the single most dangerous way to accept a git URL.
var allowedGitSchemes = map[string]bool{
	"https": true,
	"http":  true,
	"git":   true,
	"ssh":   true,
}

// gitAllowProtocol is the value pushed into GIT_ALLOW_PROTOCOL so that even if a
// malicious URL slips past ValidateGitURL, git itself refuses dangerous
// transports (ext, fd, file, …). Defense in depth.
const gitAllowProtocol = "https:http:git:ssh"

// scpLike matches git's implicit-ssh form "user@host:path" (no scheme, no "//").
var scpLike = regexp.MustCompile(`^[^/@]+@[^/:]+:`)

// ValidateGitURL rejects git URLs whose transport could execute code. It allows
// the standard schemes (https/http/git/ssh) and the scp-like ssh shorthand, and
// refuses transport-helper syntax (anything containing "::") and unknown
// schemes. addRepo calls this before ever handing a URL to `git clone`.
func ValidateGitURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty git URL")
	}
	// "transport::address" helper syntax (ext::, fd::, …) → hard no.
	if strings.Contains(raw, "::") {
		return fmt.Errorf("git transport-helper URLs are not allowed")
	}
	if scpLike.MatchString(raw) && !strings.Contains(raw, "://") {
		return nil // user@host:path — implicit ssh
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid git URL: %w", err)
	}
	if !allowedGitSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("unsupported git URL scheme %q (allowed: https, http, git, ssh)", u.Scheme)
	}
	return nil
}
