package gitstore

import (
	"fmt"
	"strconv"
	"strings"
)

// SyncReport describes one fetch/merge/push round.
type SyncReport struct {
	Fetched        bool   `json:"fetched"`
	MainMerged     bool   `json:"mainMerged"`
	MainForwarded  bool   `json:"mainForwarded"`
	ContentRebuilt bool   `json:"contentRebuilt"`
	Pushed         bool   `json:"pushed"`
	Unpushed       int    `json:"unpushed"`
	Error          string `json:"error,omitempty"`
}

func remoteRef(branch string) string { return "refs/remotes/" + Remote + "/" + branch }

// Fetch downloads both branches from origin. A missing content branch on the
// remote is not an error (a fresh remote only has main).
func (r *Repo) Fetch() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.git("fetch", "-q", Remote, "+refs/heads/"+MainBranch+":"+remoteRef(MainBranch)); err != nil {
		return err
	}
	_, _ = r.git("fetch", "-q", Remote, "+refs/heads/"+ContentBranch+":"+remoteRef(ContentBranch))
	return nil
}

// MergeMain merges origin/main into main. Event files never collide because
// their paths contain the event id, so a conflict means somebody wrote a
// non-protocol file; the merge is aborted and ErrDiverged returned.
func (r *Repo) MergeMain() (merged, forwarded bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	remote, err := r.revParse(remoteRef(MainBranch))
	if err != nil {
		return false, false, nil
	}
	local, err := r.revParse("refs/heads/" + MainBranch)
	if err != nil {
		return false, false, err
	}
	if remote == local || r.isAncestorLocked(remote, local) {
		return false, false, nil
	}
	if r.isAncestorLocked(local, remote) {
		_, err = r.git("merge", "-q", "--ff-only", remoteRef(MainBranch))
		return false, err == nil, err
	}
	if _, err = r.git("merge", "-q", "--no-ff", "--no-edit", "-m", "merge "+Remote+"/"+MainBranch, remoteRef(MainBranch)); err != nil {
		_, _ = r.git("merge", "--abort")
		return false, false, fmt.Errorf("%w: %v", ErrDiverged, err)
	}
	return true, false, nil
}

func (r *Repo) isAncestorLocked(a, b string) bool {
	_, err := r.git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// MergeContent reconciles the content branch with origin/content. Content is
// a projection, so diverged histories are never text-merged: the result is
// rebuilt as (union of blobs) minus (union of deleted ids).
func (r *Repo) MergeContent() (rebuilt bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	remote, err := r.revParse(remoteRef(ContentBranch))
	if err != nil {
		return false, nil
	}
	local, lerr := r.revParse("refs/heads/" + ContentBranch)
	if lerr != nil {
		_, err = r.git("update-ref", "refs/heads/"+ContentBranch, remote)
		return false, err
	}
	if remote == local || r.isAncestorLocked(remote, local) {
		return false, nil
	}
	if r.isAncestorLocked(local, remote) {
		_, err = r.git("update-ref", "refs/heads/"+ContentBranch, remote, local)
		return false, err
	}
	lm, err := r.readManifest(local)
	if err != nil {
		return false, err
	}
	rm, err := r.readManifest(remote)
	if err != nil {
		return false, err
	}
	blobs, err := r.allBlobs(local)
	if err != nil {
		return false, err
	}
	rb, err := r.allBlobs(remote)
	if err != nil {
		return false, err
	}
	for k, v := range rb {
		if _, ok := blobs[k]; !ok {
			blobs[k] = v
		}
	}
	gen := lm.Generation
	if rm.Generation > gen {
		gen = rm.Generation
	}
	deleted := append(append([]string(nil), lm.Deleted...), rm.Deleted...)
	return true, r.rebuildContent(blobs, deleted, gen+1, []string{local, remote})
}

// Push sends main and content. A rejected push is returned as an error so the
// caller can fetch, merge and retry.
func (r *Repo) Push() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	refs := []string{"refs/heads/" + MainBranch + ":refs/heads/" + MainBranch}
	if _, err := r.revParse("refs/heads/" + ContentBranch); err == nil {
		refs = append(refs, "refs/heads/"+ContentBranch+":refs/heads/"+ContentBranch)
	}
	_, err := r.git(append([]string{"push", "-q", Remote}, refs...)...)
	if err == nil {
		for _, b := range []string{MainBranch, ContentBranch} {
			if sha, e := r.revParse("refs/heads/" + b); e == nil {
				_, _ = r.git("update-ref", remoteRef(b), sha)
			}
		}
	}
	return err
}

// Unpushed counts main commits not yet on origin/main.
func (r *Repo) Unpushed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.revParse(remoteRef(MainBranch)); err != nil {
		out, err := r.git("rev-list", "--count", "refs/heads/"+MainBranch)
		if err != nil {
			return 0
		}
		n, _ := strconv.Atoi(strings.TrimSpace(out))
		return n
	}
	out, err := r.git("rev-list", "--count", remoteRef(MainBranch)+"..refs/heads/"+MainBranch)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// Sync runs fetch, merge and push once. Push failures are reported, not
// fatal: the next round retries after merging whatever the remote gained.
func (r *Repo) Sync() SyncReport {
	var rep SyncReport
	if err := r.Fetch(); err != nil {
		rep.Error = err.Error()
		rep.Unpushed = r.Unpushed()
		return rep
	}
	rep.Fetched = true
	merged, fwd, err := r.MergeMain()
	rep.MainMerged, rep.MainForwarded = merged, fwd
	if err != nil {
		rep.Error = err.Error()
		rep.Unpushed = r.Unpushed()
		return rep
	}
	if rep.ContentRebuilt, err = r.MergeContent(); err != nil {
		rep.Error = err.Error()
	}
	if err := r.Push(); err != nil {
		rep.Error = err.Error()
	} else {
		rep.Pushed = true
	}
	rep.Unpushed = r.Unpushed()
	return rep
}

// RemoteMain is the tracking ref of origin/main after Fetch.
func RemoteMain() string { return remoteRef(MainBranch) }

// HasRef reports whether ref resolves to a commit.
func (r *Repo) HasRef(ref string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.revParse(ref)
	return err == nil
}

// Commit is one main-branch commit, for the UI's Git view.
type Commit struct {
	SHA     string   `json:"sha"`
	Parents []string `json:"parents"`
	Time    int64    `json:"time"`
	Subject string   `json:"subject"`
	Files   []string `json:"files"`
	Pushed  bool     `json:"pushed"` // reachable from origin/main
}

// Log returns the newest n commits of main, newest first. Merge commits list
// no files (the merged events are in the commits of the other parent).
func (r *Repo) Log(n int) ([]Commit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out, err := r.git("log", "-n", strconv.Itoa(n), "--no-renames", "--name-only",
		"--format=%x1e%H%x1f%P%x1f%ct%x1f%s", "refs/heads/"+MainBranch)
	if err != nil {
		return nil, err
	}
	pushed := map[string]bool{}
	if _, err := r.revParse(remoteRef(MainBranch)); err == nil {
		if lst, err := r.git("rev-list", "-n", strconv.Itoa(n*4), remoteRef(MainBranch)); err == nil {
			for _, sha := range strings.Fields(lst) {
				pushed[sha] = true
			}
		}
	}
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.Trim(rec, "\r\n")
		if rec == "" {
			continue
		}
		lines := strings.Split(rec, "\n")
		f := strings.SplitN(lines[0], "\x1f", 4)
		if len(f) != 4 {
			continue
		}
		t, _ := strconv.ParseInt(f[2], 10, 64)
		c := Commit{SHA: f[0], Parents: strings.Fields(f[1]), Time: t, Subject: f[3], Files: []string{}, Pushed: pushed[f[0]]}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				c.Files = append(c.Files, l)
			}
		}
		if c.Parents == nil {
			c.Parents = []string{}
		}
		commits = append(commits, c)
	}
	return commits, nil
}

// RemoteHead returns origin/main after the last fetch, or "" if unknown.
func (r *Repo) RemoteHead() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	sha, err := r.revParse(remoteRef(MainBranch))
	if err != nil {
		return ""
	}
	return sha
}
