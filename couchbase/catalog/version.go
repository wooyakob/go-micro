package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Version identifies a specific revision of a catalog record. Hash is
// always present and is what Sync compares to decide whether a record
// changed. Commit and Dirty are populated best-effort when the process is
// running inside a git checkout, echoing agentc's git-commit-based
// versioning without requiring one — a deployed binary with no .git
// directory still gets a fully usable, content-hash-versioned catalog.
type Version struct {
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
	Commit    string    `json:"commit,omitempty"`
	Dirty     bool      `json:"dirty,omitempty"`
}

func newVersion(hash string) Version {
	v := Version{Hash: hash, Timestamp: time.Now().UTC()}
	if commit, dirty, ok := gitHead(); ok {
		v.Commit, v.Dirty = commit, dirty
	}
	return v
}

func hashTool(t ToolSpec) string {
	return contentHash(t.Name, t.Description, t.Parameters, t.Annotations)
}

func hashPrompt(p PromptSpec) string {
	return contentHash(p.Name, p.Description, p.Content, p.Tools, p.Output, p.Annotations)
}

// contentHash returns a stable hex-encoded SHA-256 digest over the JSON
// encoding of parts. It is deterministic across process restarts, which is
// what lets Sync tell an unchanged tool/prompt apart from a genuinely new
// revision without needing git.
func contentHash(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

var (
	gitOnce   sync.Once
	gitCommit string
	gitDirty  bool
	gitOK     bool
)

// gitHead best-effort resolves the current git commit and working-tree
// dirty state. It is resolved once per process on first use. ok is false
// when the process isn't running inside a git checkout (or git isn't
// installed) — Version then carries only its content hash.
func gitHead() (commit string, dirty bool, ok bool) {
	gitOnce.Do(func() {
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err != nil {
			return
		}
		gitCommit = strings.TrimSpace(string(out))

		status, err := exec.Command("git", "status", "--porcelain").Output()
		if err != nil {
			return
		}
		gitDirty = len(strings.TrimSpace(string(status))) > 0
		gitOK = true
	})
	return gitCommit, gitDirty, gitOK
}
