// Copyright 2016 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package populate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	git "github.com/libgit2/git2go"

	"github.com/google/slothfs/gitiles"
	"github.com/hanwen/gitfs/manifest"
)

type fileInfo struct {
	// the SHA1 of the file. This can be nil if getting it was too expensive.
	sha1 *git.Oid

	// We can't do chtimes on symlinks.
	isLink bool
}

const attrName = "user.gitsha1"

func (fi *fileInfo) getSHA1(fn string) (*git.Oid, error) {
	if fi.sha1 == nil {
		var data [40]byte
		sz, err := syscall.Getxattr(fn, attrName, data[:])
		if err != nil {
			return nil, fmt.Errorf("Getxattr(%s, %s): %v", fn, attrName, err)
		}

		oid, err := git.NewOid(string(data[:sz]))
		if err != nil {
			return nil, err
		}
		fi.sha1 = oid
	}
	return fi.sha1, nil
}

type repoTree struct {
	// repositories under this repository
	children map[string]*repoTree

	// files in this repository.
	entries map[string]*fileInfo
}

func (t *repoTree) findParentRepo(path string) (*repoTree, string) {
	for k, ch := range t.children {
		if strings.HasPrefix(path, k+"/") {
			return ch.findParentRepo(path[len(k+"/"):])
		}
	}
	return t, path
}

func (t *repoTree) write(w io.Writer, indent string) {
	for nm, ch := range t.children {
		fmt.Fprintf(w, "%s%s:\n", indent, nm)
		ch.write(w, indent+" ")
	}
}

func repoTreeFromManifest(xmlFile string) (*repoTree, error) {
	mf, err := manifest.ParseFile(xmlFile)
	if err != nil {
		return nil, err
	}

	var byDepth [][]*manifest.Project
	for i, p := range mf.Project {
		l := len(strings.Split(p.Path, "/"))
		for len(byDepth) <= l {
			byDepth = append(byDepth, nil)
		}

		byDepth[l] = append(byDepth[l], &mf.Project[i])
	}

	root := makeRepoTree()
	treesByPath := map[string]*repoTree{
		"": root,
	}

	for _, projs := range byDepth {
		for _, p := range projs {
			childTree := makeRepoTree()
			treesByPath[p.Path] = childTree

			parent, key := root.findParentRepo(p.Path)
			parent.children[key] = childTree
		}
	}

	for _, p := range mf.Project {
		for _, c := range p.Copyfile {
			root.entries[c.Dest] = &fileInfo{}
		}
		for _, c := range p.Linkfile {
			root.entries[c.Dest] = &fileInfo{}
		}
	}
	return root, nil
}

func (t *repoTree) fillFromSlothFS(dir string) error {
	c, err := ioutil.ReadFile(filepath.Join(dir, ".slothfs", "tree.json"))
	if err != nil {
		return err
	}

	var tree gitiles.Tree
	if err := json.Unmarshal(c, &tree); err != nil {
		return err
	}

	for _, e := range tree.Entries {
		fi := &fileInfo{}
		fi.sha1, err = git.NewOid(e.ID)
		if err != nil {
			return err
		}

		t.entries[e.Name] = fi

		if e.Target != nil {
			fi.isLink = true
		}
	}

	for k, v := range t.children {
		v.fillFromSlothFS(filepath.Join(dir, k))
	}

	return nil
}

func repoTreeFromSlothFS(dir string) (*repoTree, error) {
	root, err := repoTreeFromManifest(filepath.Join(dir, ".slothfs", "manifest.xml"))
	if err != nil {
		return nil, err
	}

	if err := root.fillFromSlothFS(dir); err != nil {
		return nil, err
	}
	return root, nil
}

func makeRepoTree() *repoTree {
	return &repoTree{
		children: map[string]*repoTree{},
		entries:  map[string]*fileInfo{},
	}
}

func newRepoTree(dir string) (*repoTree, error) {
	t := makeRepoTree()
	if err := t.fill(dir, ""); err != nil {
		return nil, err
	}
	return t, nil
}

// allChildren returns all the repositories (including the receiver)
// as a map keyed by relative path.
func (t *repoTree) allChildren() map[string]*repoTree {
	r := map[string]*repoTree{"": t}
	for nm, ch := range t.children {
		for sub, subCh := range ch.allChildren() {
			r[filepath.Join(nm, sub)] = subCh
		}
	}
	return r
}

// allFiles returns all the files below this repoTree.
func (t *repoTree) allFiles() map[string]*fileInfo {
	r := map[string]*fileInfo{}
	for nm, info := range t.entries {
		r[nm] = info
	}
	for nm, ch := range t.children {
		for sub, subCh := range ch.allFiles() {
			r[filepath.Join(nm, sub)] = subCh
		}
	}
	return r
}

func isRepoDir(path string) bool {
	if stat, err := os.Stat(filepath.Join(path, ".git")); err == nil && stat.IsDir() {
		return true
	} else if stat, err := os.Stat(filepath.Join(path, ".slothfs")); err == nil && stat.IsDir() {
		return true
	}
	return false
}

// construct fills `parent` looking through `dir` subdir of `repoRoot`.
func (parent *repoTree) fill(repoRoot, dir string) error {
	entries, err := ioutil.ReadDir(filepath.Join(repoRoot, dir))
	if err != nil {
		log.Println(repoRoot, err)
		return err
	}

	todo := map[string]*repoTree{}
	for _, e := range entries {
		if e.IsDir() && (e.Name() == ".git" || e.Name() == ".slothfs") {
			continue
		}
		if e.IsDir() && e.Name() == "out" && dir == "" {
			// Ignore the build output directory.
			continue
		}

		subName := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if newRoot := filepath.Join(repoRoot, subName); isRepoDir(newRoot) {
				ch := makeRepoTree()
				parent.children[subName] = ch
				todo[newRoot] = ch
			} else {
				parent.fill(repoRoot, subName)
			}
		} else {
			parent.entries[subName] = &fileInfo{}
		}
	}

	errs := make(chan error, len(todo))
	for newRoot, ch := range todo {
		go func(r string, t *repoTree) {
			errs <- t.fill(r, "")
		}(newRoot, ch)
	}

	for range todo {
		err := <-errs
		if err != nil {
			return err
		}
	}

	return nil
}

// symlinkRepo creates symlinks for all the files in `child`.
func symlinkRepo(name string, child *repoTree, roRoot, rwRoot string) error {
	fi, err := os.Stat(filepath.Join(rwRoot, name))
	if err == nil && fi.IsDir() {
		return nil
	}

	for e := range child.entries {
		dest := filepath.Join(rwRoot, name, e)

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.Symlink(filepath.Join(roRoot, name, e), dest); err != nil {
			return err
		}
	}
	return nil
}

// createTreeLinks tries to short-cut symlinks for whole trees by
// symlinking to the root of a repository in the RO tree.
func createTreeLinks(ro, rw *repoTree, roRoot, rwRoot string) error {
	allRW := rw.allChildren()

outer:
	for nm, ch := range ro.children {
		foundCheckout := false
		foundRecurse := false
		for k := range allRW {
			if k == "" {
				continue
			}
			if nm == k {
				foundRecurse = true
				break
			}
			rel, err := filepath.Rel(nm, k)
			if err != nil {
				return err
			}

			if strings.HasPrefix(rel, "..") {
				continue
			}

			// we have a checkout below "nm".
			foundCheckout = true
			break
		}

		switch {
		case foundRecurse:
			if err := createTreeLinks(ch, rw.children[nm], filepath.Join(roRoot, nm), filepath.Join(rwRoot, nm)); err != nil {
				return err
			}
			continue outer
		case !foundCheckout:
			dest := filepath.Join(rwRoot, nm)
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			if err := os.Symlink(filepath.Join(roRoot, nm), dest); err != nil {
				return err
			}
		}
	}
	return nil
}

// createLinks will populate a RW tree with symlinks to the RO tree.
func createLinks(ro, rw *repoTree, roRoot, rwRoot string) error {
	if err := createTreeLinks(ro, rw, roRoot, rwRoot); err != nil {
		return err
	}

	rwc := rw.allChildren()
	for nm, ch := range ro.allChildren() {
		if _, ok := rwc[nm]; !ok {
			if err := symlinkRepo(nm, ch, roRoot, rwRoot); err != nil {
				return err
			}
		}
	}
	return nil
}

// clearLinks removes all symlinks to the RO tree. It returns the workspace name that was linked before.
func clearLinks(mount, dir string) (string, error) {
	mount = filepath.Clean(mount)

	var prefix string
	var dirs []string
	if err := filepath.Walk(dir, func(n string, fi os.FileInfo, err error) error {
		if fi == nil {
			return fmt.Errorf("Walk %s: nil fileinfo for %s", dir, n)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(n)
			if err != nil {
				return err
			}
			if strings.HasPrefix(target, mount) {
				prefix = target
				if err := os.Remove(n); err != nil {
					return err
				}
			}
		}
		if fi.IsDir() && n != dir {
			dirs = append(dirs, n)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("Walk %s: %v", dir, err)
	}

	// Reverse the ordering, so we get the deepest subdirs first.
	sort.Strings(dirs)
	for i := range dirs {
		d := dirs[len(dirs)-1-i]
		// Ignore error: dir may still contain entries.
		os.Remove(d)
	}

	prefix = strings.TrimPrefix(prefix, mount+"/")
	if i := strings.Index(prefix, "/"); i != -1 {
		prefix = prefix[:i]
	}
	return prefix, nil
}

// Returns the filenames (as relative paths) in newDir that have
// changed relative to the files in oldDir.
func changedFiles(oldInfos map[string]*fileInfo, newInfos map[string]*fileInfo) ([]string, error) {
	var changed []string
	for path, info := range newInfos {
		old, ok := oldInfos[path]
		if !ok {
			changed = append(changed, path)
			continue
		}
		if info.isLink {
			// TODO(hanwen): maybe we should we deref the link?
			continue
		}

		if old.sha1 == nil || info.sha1 == nil {
			changed = append(changed, path)
			continue
		}
		if bytes.Compare(old.sha1[:], info.sha1[:]) != 0 {
			changed = append(changed, path)
			continue
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// Checkout updates a RW dir with new symlinks to the given RO dir.
// Returns the files that should be touched.
func Checkout(ro, rw string) ([]string, error) {
	ro = filepath.Clean(ro)
	wsName, err := clearLinks(filepath.Dir(ro), rw)
	if err != nil {
		return nil, err
	}
	oldRoot := filepath.Join(filepath.Dir(ro), wsName)

	// Do the file system traversals in parallel.
	errs := make(chan error, 3)
	var rwTree, roTree *repoTree
	var oldInfos map[string]*fileInfo

	if wsName != "" {
		go func() {
			t, err := repoTreeFromSlothFS(oldRoot)
			if t != nil {
				oldInfos = t.allFiles()
			}
			errs <- err
		}()
	} else {
		oldInfos = map[string]*fileInfo{}
		errs <- nil
	}

	go func() {
		t, err := newRepoTree(rw)
		rwTree = t
		errs <- err
	}()
	go func() {
		t, err := repoTreeFromSlothFS(ro)
		roTree = t
		errs <- err
	}()

	for i := 0; i < cap(errs); i++ {
		err := <-errs
		if err != nil {
			return nil, err
		}
	}

	if err := createLinks(roTree, rwTree, ro, rw); err != nil {
		return nil, err
	}

	newInfos := roTree.allFiles()
	changed, err := changedFiles(oldInfos, newInfos)
	if err != nil {
		return nil, fmt.Errorf("changedFiles: %v", err)
	}

	for i, p := range changed {
		changed[i] = filepath.Join(ro, p)
	}

	return changed, nil
}
