package runner

import (
	"slices"
	"testing"
)

func TestHostKeyCheckingEnv(t *testing.T) {
	cases := map[string][]string{
		"":           nil,
		"default":    nil, // unknown → respect ansible.cfg
		"disabled":   {"ANSIBLE_HOST_KEY_CHECKING=False"},
		"accept-new": {"ANSIBLE_SSH_EXTRA_ARGS=-o StrictHostKeyChecking=accept-new"},
	}
	for mode, want := range cases {
		if got := hostKeyCheckingEnv(mode); !slices.Equal(got, want) {
			t.Errorf("hostKeyCheckingEnv(%q) = %v, want %v", mode, got, want)
		}
	}
}
