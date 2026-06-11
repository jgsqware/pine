package scanner

import (
	"reflect"
	"testing"
)

func TestEvalCondition(t *testing.T) {
	vars := map[string]any{
		"services": []any{"docker"},
		"env":      "production",
		"ansible_facts": map[string]any{
			"os_family": "Debian",
		},
		"port": float64(8080),
	}
	cases := []struct {
		expr        string
		want        Tri
		wantMissing []string
	}{
		// resolved
		{`env == 'production'`, True, nil},
		{`'docker' in services`, True, nil},
		{`ansible_facts['os_family'] == 'Debian'`, True, nil},
		{`ansible_facts.os_family == 'RedHat'`, False, nil},
		{`port > 8000`, True, nil},
		{`port <= 80`, False, nil},
		{`missing is defined`, False, nil},
		{`missing is not defined`, True, nil},
		{`'x' in (missing | default([]))`, False, nil},
		// unknown + reported vars
		{`ansible_distribution == 'Ubuntu'`, Unknown, []string{"ansible_distribution"}},
		{`ansible_facts['distribution'] == 'Ubuntu'`, Unknown, []string{"ansible_facts.distribution"}},
		{`other_facts['os_family'] == 'Debian'`, Unknown, []string{"other_facts.os_family"}},
		{`'docker' in missing_list`, Unknown, []string{"missing_list"}},
		{`missing_a == missing_b`, Unknown, []string{"missing_a", "missing_b"}},
		// three-valued shortcuts: a known false/true side decides
		{`env == 'staging' and missing_var == 'x'`, False, nil},
		{`env == 'production' or missing_var == 'x'`, True, nil},
		{`env == 'production' and missing_var == 'x'`, Unknown, []string{"missing_var"}},
		{`not missing_var`, Unknown, []string{"missing_var"}},
		// unsupported syntax degrades to unknown, never errors
		{`hostvars[item].weird | selectattr('x')`, Unknown, nil},
	}
	for _, c := range cases {
		got, missing := EvalCondition(c.expr, vars)
		if got != c.want {
			t.Errorf("EvalCondition(%q) = %v, want %v", c.expr, got, c.want)
		}
		if !reflect.DeepEqual(missing, c.wantMissing) {
			t.Errorf("EvalCondition(%q) missing = %v, want %v", c.expr, missing, c.wantMissing)
		}
	}
}

func TestInterpolate(t *testing.T) {
	vars := map[string]any{"app_version": "2.4.1", "name": "shop"}

	out, known, missing := Interpolate("Deploy {{ name }} v{{ app_version }}", vars)
	if !known || out != "Deploy shop v2.4.1" || missing != nil {
		t.Errorf("got %q known=%v missing=%v", out, known, missing)
	}

	out, known, missing = Interpolate("Deploy {{ name }} to {{ target_env }}", vars)
	if known || out != "Deploy shop to {{ target_env }}" {
		t.Errorf("got %q known=%v", out, known)
	}
	if len(missing) != 1 || missing[0] != "target_env" {
		t.Errorf("missing = %v", missing)
	}

	out, known, _ = Interpolate("no templates here", vars)
	if !known || out != "no templates here" {
		t.Errorf("got %q known=%v", out, known)
	}
}
