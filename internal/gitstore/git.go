// Package gitstore keeps the room repository: the append-only main branch of
// signed events (a normal checked-out worktree) and the rebuildable content
// branch of message bodies (written with plumbing commands, never checked
// out). All Git work goes through the system git binary and one mutex, so
// writes, syncs and content rebuilds are serialised per node.
package gitstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	MainBranch    = "main"
	ContentBranch = "content"
	Remote        = "origin"
)

// Repo wraps one local clone of a room repository.
type Repo struct {
	Dir string
	mu  sync.Mutex
}

// ErrNoCommit is returned when a ref does not exist yet.
var ErrNoCommit = errors.New("gitstore: ref does not exist")

func (r *Repo) run(ctx context.Context, stdin []byte, env []string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.Dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=gitchain", "GIT_AUTHOR_EMAIL=node@gitchain.invalid",
		"GIT_COMMITTER_NAME=gitchain", "GIT_COMMITTER_EMAIL=node@gitchain.invalid",
	)
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (r *Repo) git(args ...string) (string, error) {
	out, err := r.run(nil, nil, nil, args...)
	return strings.TrimRight(out, "\r\n"), err
}

// Init creates a new repository with the genesis commit on main and an empty
// content branch. Commit identities are fixed so they never leak user data.
func Init(dir string, roomMeta []byte) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &Repo{Dir: dir}
	if _, err := r.git("init", "-q", "-b", MainBranch); err != nil {
		return nil, err
	}
	if err := r.configure(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta", "room.json"), roomMeta, 0o644); err != nil {
		return nil, err
	}
	if _, err := r.git("add", "meta/room.json"); err != nil {
		return nil, err
	}
	if _, err := r.git("commit", "-q", "-m", "genesis"); err != nil {
		return nil, err
	}
	if err := r.writeContent(map[string][]byte{"manifest.json": []byte(`{"generation":0,"deleted":[]}`)}, nil); err != nil {
		return nil, err
	}
	return r, nil
}

// Clone clones url into dir and tracks the content branch.
func Clone(url, dir string) (*Repo, error) {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "clone", "-q", "-b", MainBranch, url, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone: %w: %s", err, out)
	}
	r := &Repo{Dir: dir}
	if err := r.configure(); err != nil {
		return nil, err
	}
	if _, err := r.git("fetch", "-q", Remote, "+refs/heads/"+ContentBranch+":refs/remotes/"+Remote+"/"+ContentBranch); err == nil {
		_, _ = r.git("update-ref", "refs/heads/"+ContentBranch, "refs/remotes/"+Remote+"/"+ContentBranch)
	}
	return r, nil
}

// Open opens an existing clone.
func Open(dir string) (*Repo, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil, fmt.Errorf("gitstore: %s is not a repository: %w", dir, err)
	}
	return &Repo{Dir: dir}, nil
}

func (r *Repo) configure() error {
	for _, kv := range [][2]string{
		{"core.autocrlf", "false"},
		{"merge.ff", "true"},
		{"gc.auto", "0"},
		{"commit.gpgsign", "false"},
	} {
		if _, err := r.git("config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// AddRemote sets origin.
func (r *Repo) AddRemote(url string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.git("remote", "get-url", Remote); err == nil {
		_, err = r.git("remote", "set-url", Remote, url)
		return err
	}
	_, err := r.git("remote", "add", Remote, url)
	return err
}

// Head returns the commit of the main branch.
func (r *Repo) Head() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revParse("refs/heads/" + MainBranch)
}

func (r *Repo) revParse(ref string) (string, error) {
	out, err := r.git("rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil || out == "" {
		return "", ErrNoCommit
	}
	return out, nil
}

// HasCommit reports whether sha is a commit object in the local repository.
func (r *Repo) HasCommit(sha string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.git("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// IsAncestor reports whether ancestor is reachable from descendant.
func (r *Repo) IsAncestor(ancestor, descendant string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.git("merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// ListFiles lists paths under prefix at rev.
func (r *Repo) ListFiles(rev, prefix string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.listFiles(rev, prefix)
}

func (r *Repo) listFiles(rev, prefix string) ([]string, error) {
	out, err := r.git("ls-tree", "-r", "--name-only", rev, "--", prefix)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// ReadFile reads path at rev.
func (r *Repo) ReadFile(rev, path string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readFile(rev, path)
}

func (r *Repo) readFile(rev, path string) ([]byte, error) {
	out, err := r.run(nil, nil, nil, "cat-file", "blob", rev+":"+path)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// TreeEntry is one file of a tree listing.
type TreeEntry struct {
	Path string
	Blob string
}

// LsTree lists files under prefix at rev with their blob ids.
func (r *Repo) LsTree(rev, prefix string) ([]TreeEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.git("ls-tree", "-r", rev, "--", prefix)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	var entries []TreeEntry
	for _, line := range strings.Split(out, "\n") {
		// "<mode> blob <sha>\t<path>"
		tab := strings.IndexByte(line, '\t')
		fields := strings.Fields(line[:max(tab, 0)])
		if tab < 0 || len(fields) != 3 || fields[1] != "blob" {
			continue
		}
		entries = append(entries, TreeEntry{Path: line[tab+1:], Blob: fields[2]})
	}
	return entries, nil
}

// CatBlob reads a blob object by id.
func (r *Repo) CatBlob(sha string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.run(nil, nil, nil, "cat-file", "blob", sha)
	return []byte(out), err
}
