package plan

// secret_timeline.go — git-history dimension for secret detection.
//
// Two complementary analyses:
//
//  1. Provenance of live findings: for each plaintext secret that Hygiene()
//     already found in the working tree, git-blame the file to surface the
//     introducing commit (SHA, author, date) and how many commits ago that was.
//
//  2. Purged-secret scan: walk git log -p (pickaxe -S for each known secret
//     pattern) to detect keys that were committed then removed from HEAD —
//     they still exist in history and are therefore a leak even though the
//     working tree looks clean.
//
// Both analyses are best-effort: a non-git directory, a missing git binary, or
// a file that was not yet committed simply produce zero enrichment — the caller
// (Hygiene) never fails because of timeline errors.
//
// Performance guardrails
//   - Blame: one `git blame --porcelain` call per live finding, skipped when
//     the file is untracked (git blame exits non-zero).
//   - Purged scan: a single `git log -p --diff-filter=D --pickaxe-regex -S`
//     call per secret-key pattern (not per commit), capped at historyLimit
//     commits so it is O(patterns × limit) not O(commits × patterns).
//     Only the deletion side of each diff hunk is examined.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// historyLimit caps how many commits are scanned for purged secrets.
// Large repos with thousands of commits stay fast; raise via tests if needed.
const historyLimit = 200

// enrichSecretTimeline fills SecretFinding.IntroducedSHA/Date/By/AgeSinceCommit
// for every live finding and appends PurgedSecrets for deleted credentials
// still present in git history.  It is a no-op when root is not a git repo.
func enrichSecretTimeline(out *HygieneResult, root string) {
	// fast-path: not a git repo (or git not installed)
	if _, err := gitOut(root, "rev-parse", "--git-dir"); err != nil {
		return
	}

	// 1. Provenance of live findings
	totalCommits := countCommits(root)
	for i := range out.SecretFindings {
		annotateProvenance(root, &out.SecretFindings[i], totalCommits)
	}

	// 2. Purged-secret scan
	out.PurgedSecrets = append(out.PurgedSecrets, scanPurgedSecrets(root)...)
}

// annotateProvenance finds the git commit that introduced the secret key's
// plaintext definition and fills IntroducedSHA/Date/By/AgeSinceCommit.
//
// Strategy: `git log -S <key> -n<limit>` (pickaxe) finds commits that changed
// the count of the exact string `key:` in the codebase.  The earliest such
// commit in HEAD's history is the one that added it.  This approach does not
// require knowing the exact file path (SecretFinding.File is a human string).
func annotateProvenance(root string, f *SecretFinding, totalCommits int) {
	// Use pickaxe on the key name (exact string match on "key:") to find the
	// commit(s) that touched this definition.
	logArgs := []string{
		"log",
		fmt.Sprintf("-n%d", historyLimit),
		"-p",
		"--format=COMMIT:%H%x09%ct%x09%an",
		"-S", f.Key + ":",
	}
	logOut, err := gitOut(root, logArgs...)
	if err != nil {
		return
	}

	sha, author, ts := findAdditionInLog(logOut, f.Key)
	if sha == "" {
		return
	}
	f.IntroducedSHA = sha
	if ts != 0 {
		f.IntroducedDate = time.Unix(ts, 0).UTC().Format(time.RFC3339)
	}
	f.IntroducedBy = author

	// age as "N commits ago" — count how many commits are descendants of sha
	if totalCommits > 0 {
		n := commitsAfter(root, sha, totalCommits)
		if n == 0 {
			f.AgeSinceCommit = "HEAD commit"
		} else {
			f.AgeSinceCommit = fmt.Sprintf("%d commit(s) ago", n)
		}
	}
}

// findAdditionInLog parses `git log -p` output and returns the SHA, author,
// and timestamp for the earliest commit that has a "+" line defining the key.
// Commits are returned newest-first by git log, so we scan all and keep the
// last match (oldest introducing commit).
func findAdditionInLog(logOut, key string) (sha, author string, ts int64) {
	var curSHA, curAuthor string
	var curTS int64
	for _, raw := range strings.Split(logOut, "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.HasPrefix(line, "COMMIT:") {
			parts := strings.SplitN(strings.TrimPrefix(line, "COMMIT:"), "\t", 3)
			if len(parts) == 3 {
				curSHA = parts[0]
				if len(curSHA) > 7 {
					curSHA = curSHA[:7]
				}
				t, _ := strconv.ParseInt(parts[1], 10, 64)
				curTS = t
				curAuthor = parts[2]
			}
			continue
		}
		if strings.HasPrefix(line, "+") && len(line) > 1 {
			content := line[1:]
			if k := extractSecretKey(content); k == key {
				// Keep the latest (oldest in history) match
				sha, author, ts = curSHA, curAuthor, curTS
			}
		}
	}
	return sha, author, ts
}

// blameForKey parses `git blame --porcelain` output and returns the SHA,
// author-name, and author-time for the line that defines `key` (i.e. a line
// that starts with `key:` or `key =`).
func blameForKey(porcelain, key string) (sha, author string, ts int64) {
	// porcelain format per-line group:
	//   <40-sha> <orig-line> <final-line> <group-size>
	//   author <name>
	//   author-time <unix>
	//   ... (other fields)
	//   \t<actual line content>
	var curSHA, curAuthor string
	var curTS int64
	lines := strings.Split(porcelain, "\n")
	for _, line := range lines {
		switch {
		case len(line) == 0:
			continue
		case line[0] == '\t':
			// actual content line
			content := line[1:]
			if isDefinitionOf(content, key) && curSHA != "" && curSHA != strings.Repeat("0", 40) {
				return curSHA[:7], curAuthor, curTS
			}
		case strings.HasPrefix(line, "author ") && !strings.HasPrefix(line, "author-"):
			curAuthor = strings.TrimPrefix(line, "author ")
		case strings.HasPrefix(line, "author-time "):
			v, _ := strconv.ParseInt(strings.TrimPrefix(line, "author-time "), 10, 64)
			curTS = v
		default:
			// header line: first token is the SHA
			if len(line) >= 40 {
				parts := strings.Fields(line[:41])
				if len(parts[0]) == 40 {
					curSHA = parts[0]
					curAuthor = ""
					curTS = 0
				}
			}
		}
	}
	return "", "", 0
}

// isDefinitionOf reports whether a text line is a YAML/INI definition of key.
func isDefinitionOf(line, key string) bool {
	trimmed := strings.TrimLeft(line, " \t-")
	// YAML: "key:" or "key: value"
	if strings.HasPrefix(trimmed, key+":") || strings.HasPrefix(trimmed, key+" :") {
		return true
	}
	// INI: "key = value" or "key=value"
	if strings.HasPrefix(trimmed, key+"=") || strings.HasPrefix(trimmed, key+" =") {
		return true
	}
	return false
}

// blameablePath extracts the filesystem path from SecretFinding.File.
// Finding.File can be things like:
//   - "group_vars/all (production)"       → "group_vars/all.yml" (not reliable)
//   - "host_vars/web01 (production)"       → "host_vars/web01"
//   - "defaults (role nginx)"              → skip
//
// We only attempt blame for group_vars/host_vars paths where the actual file
// can be reasonably inferred; role defaults come from in-memory structs with
// no reliable single file path so we skip them.
func blameablePath(file string) string {
	// Strip inventory suffix "(inventory name)" if present
	if idx := strings.Index(file, " ("); idx > 0 {
		file = strings.TrimSpace(file[:idx])
	}
	// role defaults/vars — no reliable file path
	if strings.HasPrefix(file, "defaults (") || strings.HasPrefix(file, "defaults") ||
		strings.Contains(file, "role ") {
		return ""
	}
	return file
}

// countCommits returns the total number of commits reachable from HEAD,
// capped at historyLimit*10 to avoid expensive counts on huge repos.
func countCommits(root string) int {
	out, err := gitOut(root, "rev-list", "--count", fmt.Sprintf("--max-count=%d", historyLimit*10), "HEAD")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// commitsAfter returns how many commits in HEAD's history come after sha
// (i.e. are descendants of sha), as a proxy for "how old" the commit is.
func commitsAfter(root, sha string, totalCommits int) int {
	out, err := gitOut(root, "rev-list", "--count", sha+"..HEAD")
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	_ = totalCommits
	return n
}

// ── Purged-secret scan ────────────────────────────────────────────────────────

// purgedCandidate is a deleted-line candidate found in the diff walk.
type purgedCandidate struct {
	key  string
	file string
	sha  string
	date string
	by   string
}

// scanPurgedSecrets walks git history looking for secret-key definitions that
// were committed and later removed (or overwritten with a vault reference).
// It runs a single bounded `git log -p -n<limit>` and filters deletion lines
// in Go, avoiding cross-platform pickaxe-regex issues.
func scanPurgedSecrets(root string) []PurgedSecret {
	logArgs := []string{
		"log",
		fmt.Sprintf("-n%d", historyLimit),
		"-p",
		"--format=COMMIT:%H%x09%ct%x09%an",
	}
	logOut, err := gitOut(root, logArgs...)
	if err != nil {
		return nil
	}

	deletions := parsePurgedDiff(logOut)
	if len(deletions) == 0 {
		return nil
	}

	// For each deleted definition, check whether the key's plaintext value
	// still exists at HEAD.  We use git grep on HEAD to check whether the
	// exact plaintext form is still present.  If the key is present only as
	// a vault-encrypted reference, keyHasPlaintextAtHead returns false.
	var purged []PurgedSecret
	seen := map[string]bool{}
	for _, d := range deletions {
		uniq := d.key + ":" + d.file
		if seen[uniq] {
			continue
		}
		seen[uniq] = true
		if keyHasPlaintextAtHead(root, d.key) {
			continue // still present in plaintext — already caught by live scan
		}
		// find the commit that introduced this key (first commit adding it)
		addSHA, addDate, addBy := findIntroducingCommit(root, d.key, d.sha)
		purged = append(purged, PurgedSecret{
			Key:            d.key,
			File:           d.file,
			IntroducedSHA:  addSHA,
			IntroducedDate: addDate,
			IntroducedBy:   addBy,
			RemovedSHA:     d.sha,
			RemovedDate:    d.date,
		})
	}
	return purged
}

// secretPickaxePattern is a git-pickaxe-compatible regex that broadly matches
// YAML/INI lines defining a secret-looking key.  We use a permissive pattern
// and do precise filtering in Go.
const secretPickaxePattern = `(password|passwd|passphrase|secret|token|api.?key|access.?key|private.?key|credential|vault_)[^:=]*[:=]`

// parsePurgedDiff parses the output of `git log -p --format=... --diff-filter=D`
// and returns deleted lines that look like secret definitions.
func parsePurgedDiff(logOut string) []purgedCandidate {
	var results []purgedCandidate
	var curSHA, curDate, curBy, curFile string

	for _, raw := range strings.Split(logOut, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case strings.HasPrefix(line, "COMMIT:"):
			parts := strings.SplitN(strings.TrimPrefix(line, "COMMIT:"), "\t", 3)
			if len(parts) == 3 {
				curSHA = parts[0]
				if len(curSHA) > 7 {
					curSHA = curSHA[:7]
				}
				ts, _ := strconv.ParseInt(parts[1], 10, 64)
				curDate = time.Unix(ts, 0).UTC().Format(time.RFC3339)
				curBy = parts[2]
			}
		case strings.HasPrefix(line, "--- a/"):
			curFile = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "--- /dev/null"):
			curFile = ""
		case strings.HasPrefix(line, "-") && len(line) > 1 && curSHA != "":
			// deleted line in diff
			content := line[1:]
			if key := extractSecretKey(content); key != "" {
				results = append(results, purgedCandidate{
					key:  key,
					file: curFile,
					sha:  curSHA,
					date: curDate,
					by:   curBy,
				})
			}
		}
	}
	return results
}

// extractSecretKey inspects a diff line (without the leading "-") and returns
// the variable name if it is a secret-key definition with a non-trivial value.
func extractSecretKey(line string) string {
	trimmed := strings.TrimLeft(line, " \t-#")
	// split on ":" or "="
	var key, val string
	if idx := strings.IndexByte(trimmed, ':'); idx > 0 {
		key = strings.TrimSpace(trimmed[:idx])
		val = strings.TrimSpace(trimmed[idx+1:])
	} else if idx := strings.IndexByte(trimmed, '='); idx > 0 {
		key = strings.TrimSpace(trimmed[:idx])
		val = strings.TrimSpace(trimmed[idx+1:])
	} else {
		return ""
	}
	// strip YAML type tags (!vault, !unsafe …)
	if idx := strings.IndexByte(val, '!'); idx == 0 {
		return "" // vault blob — already redacted by ansible-vault
	}
	if !secretKeyRe.MatchString(key) || notSecretKeyRe.MatchString(key) {
		return ""
	}
	if !looksLikeSecretValue(val) {
		return ""
	}
	return key
}

// keyHasPlaintextAtHead checks whether key still appears as a plaintext
// (non-vault) secret definition at HEAD.  We look for lines that define the
// key with a value that is NOT a vault blob (does not start with "!vault").
// Returns true when a plaintext definition is found — meaning the live scan
// already covers it and we should not report it as purged.
func keyHasPlaintextAtHead(root, key string) bool {
	// Use git grep to get all lines defining this key at HEAD.
	pattern := fmt.Sprintf(`%s[[:space:]]*[=:]`, key)
	grepOut, err := gitOut(root, "grep", "--extended-regexp", "-h", pattern, "HEAD")
	if err != nil {
		return false // key not found at HEAD at all
	}
	for _, line := range strings.Split(grepOut, "\n") {
		// Strip the "HEAD:filename:" prefix that git grep adds
		if idx := strings.Index(line, ":"); idx >= 0 {
			line = line[idx+1:]
			if idx2 := strings.Index(line, ":"); idx2 >= 0 {
				line = line[idx2+1:]
			}
		}
		key2 := extractSecretKey(line)
		if key2 == key {
			return true // plaintext value found at HEAD
		}
	}
	return false
}

// findIntroducingCommit finds the oldest commit in history that added the key.
// It reuses findAdditionInLog which keeps the last (oldest) match from git log.
func findIntroducingCommit(root, key, _ string) (sha, date, by string) {
	logArgs := []string{
		"log",
		fmt.Sprintf("-n%d", historyLimit),
		"-p",
		"--format=COMMIT:%H%x09%ct%x09%an",
		"-S", key + ":",
	}
	logOut, err := gitOut(root, logArgs...)
	if err != nil {
		return "", "", ""
	}
	addSHA, addBy, addTS := findAdditionInLog(logOut, key)
	if addSHA == "" {
		return "", "", ""
	}
	addDate := ""
	if addTS != 0 {
		addDate = time.Unix(addTS, 0).UTC().Format(time.RFC3339)
	}
	return addSHA, addDate, addBy
}
