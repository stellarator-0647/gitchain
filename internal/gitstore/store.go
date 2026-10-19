package gitstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EventPath returns the main-branch path of an event file:
// events/<author lower hex>/<seq>/<eventId lower hex>.json.
func EventPath(author string, seq uint64, eventID string) string {
	return fmt.Sprintf("events/%s/%d/%s.json", strings.ToLower(author), seq, strings.ToLower(eventID))
}

// BlobPath returns the content-branch path of a message body.
func BlobPath(eventID string) string { return "blobs/" + strings.ToLower(eventID) + ".json" }

// CommitEvent writes one signed event file into the main worktree and commits
// it. The commit message carries only the event id, never body text.
func (r *Repo) CommitEvent(path string, data []byte, eventID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	full := filepath.Join(r.Dir, filepath.FromSlash(path))
	if _, err := os.Stat(full); err == nil {
		return "", fmt.Errorf("gitstore: %s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	if _, err := r.git("add", "--", path); err != nil {
		return "", err
	}
	if _, err := r.git("commit", "-q", "-m", "event "+eventID); err != nil {
		return "", err
	}
	return r.revParse("refs/heads/" + MainBranch)
}

// Manifest is content/manifest.json.
type Manifest struct {
	Generation uint64   `json:"generation"`
	Deleted    []string `json:"deleted"`
}

// ContentHead returns the content branch commit.
func (r *Repo) ContentHead() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revParse("refs/heads/" + ContentBranch)
}

// ReadBlob returns the stored body file for eventID, or ErrNoCommit/os error
// when it is absent.
func (r *Repo) ReadBlob(eventID string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.revParse("refs/heads/" + ContentBranch); err != nil {
		return nil, err
	}
	return r.readFile("refs/heads/"+ContentBranch, BlobPath(eventID))
}

// ReadManifest returns the manifest at rev (a ref or commit on content).
func (r *Repo) ReadManifest(rev string) (Manifest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readManifest(rev)
}

func (r *Repo) readManifest(rev string) (Manifest, error) {
	var m Manifest
	raw, err := r.readFile(rev, "manifest.json")
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

// AddBlob stores one body on the content branch on top of its current head.
func (r *Repo) AddBlob(eventID string, data []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	head, err := r.revParse("refs/heads/" + ContentBranch)
	if err != nil {
		return err
	}
	tree, err := r.buildTree(head, map[string][]byte{BlobPath(eventID): data}, nil)
	if err != nil {
		return err
	}
	return r.commitContent(tree, []string{head}, "blob "+eventID, head)
}

// RebuildContent writes a fresh content snapshot: every given blob except the
// deleted ones, a new manifest, and the given parents. It is used after a
// diverged sync and after an authorised deletion.
func (r *Repo) RebuildContent(blobs map[string][]byte, deleted []string, generation uint64, parents []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuildContent(blobs, deleted, generation, parents)
}

func (r *Repo) rebuildContent(blobs map[string][]byte, deleted []string, generation uint64, parents []string) error {
	del := map[string]bool{}
	for _, d := range deleted {
		del[strings.ToLower(d)] = true
	}
	sorted := append([]string(nil), deleted...)
	sort.Strings(sorted)
	files := map[string][]byte{}
	for id, b := range blobs {
		if !del[strings.ToLower(id)] {
			files[BlobPath(id)] = b
		}
	}
	m, _ := json.Marshal(Manifest{Generation: generation, Deleted: dedupe(sorted)})
	files["manifest.json"] = m
	old, _ := r.revParse("refs/heads/" + ContentBranch)
	return r.writeContentLocked(files, parents, old, fmt.Sprintf("content generation %d", generation))
}

// writeContent is used by Init before the mutex matters.
func (r *Repo) writeContent(files map[string][]byte, parents []string) error {
	return r.writeContentLocked(files, parents, "", "content genesis")
}

func (r *Repo) writeContentLocked(files map[string][]byte, parents []string, old, msg string) error {
	tree, err := r.buildTree("", files, nil)
	if err != nil {
		return err
	}
	return r.commitContent(tree, parents, msg, old)
}

// AllBlobs reads every blob on a content revision, keyed by event id.
func (r *Repo) AllBlobs(rev string) (map[string][]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allBlobs(rev)
}

func (r *Repo) allBlobs(rev string) (map[string][]byte, error) {
	paths, err := r.listFiles(rev, "blobs")
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(paths))
	for _, p := range paths {
		id := strings.TrimSuffix(strings.TrimPrefix(p, "blobs/"), ".json")
		b, err := r.readFile(rev, p)
		if err != nil {
			return nil, err
		}
		out[id] = b
	}
	return out, nil
}

func (r *Repo) buildTree(base string, add map[string][]byte, remove []string) (string, error) {
	index := filepath.Join(r.Dir, ".git", "gitchain-content.index")
	_ = os.Remove(index)
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if base != "" {
		if _, err := r.run(nil, nil, env, "read-tree", base); err != nil {
			return "", err
		}
	} else if _, err := r.run(nil, nil, env, "read-tree", "--empty"); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(add))
	for p := range add {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		sha, err := r.run(nil, add[p], nil, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := r.run(nil, nil, env, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(sha)+","+p); err != nil {
			return "", err
		}
	}
	for _, p := range remove {
		if _, err := r.run(nil, nil, env, "update-index", "--force-remove", p); err != nil {
			return "", err
		}
	}
	tree, err := r.run(nil, nil, env, "write-tree")
	return strings.TrimSpace(tree), err
}

func (r *Repo) commitContent(tree string, parents []string, msg, old string) error {
	args := []string{"commit-tree", tree, "-m", msg}
	for _, p := range parents {
		if p != "" {
			args = append(args, "-p", p)
		}
	}
	commit, err := r.git(args...)
	if err != nil {
		return err
	}
	upd := []string{"update-ref", "refs/heads/" + ContentBranch, commit}
	if old != "" {
		upd = append(upd, old)
	}
	_, err = r.git(upd...)
	return err
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// ErrDiverged signals that the main branch could not be merged cleanly.
var ErrDiverged = errors.New("gitstore: main branch merge conflict")
