package plan

import "testing"

// fixture: trimmed output of ANSIBLE_STDOUT_CALLBACK=json ansible-playbook --check
const ansibleJSONFixture = `
{
  "plays": [
    {
      "play": {"name": "Configure web"},
      "tasks": [
        {"task": {"name": "Install nginx"},
         "hosts": {
           "web01": {"changed": true, "action": "apt"},
           "web02": {"changed": false, "action": "apt"}
         }},
        {"task": {"name": "Debian only"},
         "hosts": {
           "web01": {"changed": false, "skipped": true},
           "web02": {"changed": false, "skipped": true}
         }},
        {"task": {"name": "Broken"},
         "hosts": {
           "web01": {"changed": false, "failed": true, "msg": "boom"}
         }}
      ]
    }
  ],
  "stats": {"web01": {"ok": 1, "changed": 1}}
}`

func TestParseAnsibleJSON(t *testing.T) {
	out, err := parseAnsibleJSON([]byte(ansibleJSONFixture))
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "exact" || len(out.Plays) != 1 {
		t.Fatalf("mode/plays = %s/%d", out.Mode, len(out.Plays))
	}
	pp := out.Plays[0]
	if len(pp.Tasks) != 3 || out.Summary.Hosts != 2 {
		t.Fatalf("tasks=%d hosts=%d", len(pp.Tasks), out.Summary.Hosts)
	}
	install := pp.Tasks[0]
	if install.Hosts["web01"].Reason != "would change" || install.Hosts["web02"].Reason != "no change" {
		t.Errorf("install verdicts: %+v", install.Hosts)
	}
	if install.Module != "apt" {
		t.Errorf("module = %s", install.Module)
	}
	if pp.Tasks[1].Counts.Skip != 2 {
		t.Errorf("skip counts = %+v", pp.Tasks[1].Counts)
	}
	broken := pp.Tasks[2]
	if broken.Hosts["web01"].Status != StatusUnknown || broken.Hosts["web01"].Reason != "failed: boom" {
		t.Errorf("failed verdict: %+v", broken.Hosts["web01"])
	}
	// garbage before the JSON (warnings on stderr/stdout) must be tolerated
	if _, err := parseAnsibleJSON([]byte("[WARNING]: bla\n" + ansibleJSONFixture)); err != nil {
		t.Errorf("prefix garbage should be tolerated: %v", err)
	}
}
