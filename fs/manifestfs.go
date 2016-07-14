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

package fs

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/google/slothfs/cache"
	"github.com/google/slothfs/gitiles"
	"github.com/google/slothfs/manifest"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	git "github.com/libgit2/git2go"
)

type manifestFSRoot struct {
	nodefs.Node

	service *gitiles.Service

	cache     *cache.Cache
	nodeCache *nodeCache

	// trees is Path => Tree map.
	trees map[string]*gitiles.Tree

	options ManifestOptions

	// XML data for the manifest.
	manifestXML []byte
}

func (r *manifestFSRoot) Deletable() bool { return false }

func (r *manifestFSRoot) GetXAttr(attribute string, context *fuse.Context) (data []byte, code fuse.Status) {
	return nil, fuse.ENODATA
}

// NewManifestFS creates a Manifest FS root node.
func NewManifestFS(service *gitiles.Service, cache *cache.Cache, opts ManifestOptions) (nodefs.Node, error) {
	xml, err := opts.Manifest.MarshalXML()
	if err != nil {
		return nil, err
	}
	root := &manifestFSRoot{
		Node:        newDirNode(),
		nodeCache:   newNodeCache(),
		cache:       cache,
		service:     service,
		options:     opts,
		manifestXML: xml,
	}

	for _, p := range opts.Manifest.Project {
		if _, err := git.NewOid(p.Revision); err != nil {
			return nil, fmt.Errorf("project %s revision %q does not parse: %v", p.Name, p.Revision, err)
		}
	}

	root.trees, err = fetchTreeMap(cache, service, opts.Manifest)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func (fs *manifestFSRoot) OnMount(fsConn *nodefs.FileSystemConnector) {
	if err := fs.onMount(fsConn); err != nil {
		log.Printf("onMount: %v", err)
		for k := range fs.Inode().Children() {
			fs.Inode().RmChild(k)
		}

		fs.Inode().NewChild("ERROR", false, newDataNode([]byte(err.Error())))
	}

	// Don't need the trees anymore.
	fs.trees = nil
}

func (fs *manifestFSRoot) onMount(fsConn *nodefs.FileSystemConnector) error {
	var byDepth [][]string
	for p := range fs.trees {
		d := len(strings.Split(p, "/"))
		for len(byDepth) <= d {
			byDepth = append(byDepth, nil)
		}

		byDepth[d] = append(byDepth[d], p)
	}

	clonablePaths := map[string]bool{}
	revmap := map[string]*manifest.Project{}
	for i, p := range fs.options.Manifest.Project {
		revmap[p.Path] = &fs.options.Manifest.Project[i]

		if p.CloneDepth == "" {
			clonablePaths[p.Path] = true
		}
	}

	// TODO(hanwen): use parallelism here.

	for _, ps := range byDepth {
		for _, p := range ps {
			dir, base := filepath.Split(p)
			parent, left := fsConn.Node(fs.Inode(), dir)
			for _, c := range left {
				ch := parent.NewChild(c, true, newDirNode())
				parent = ch
			}

			clone, ok := clonablePaths[p]
			if !ok {
				for _, e := range fs.options.RepoCloneOption {
					if e.RE.FindString(p) != "" {
						clone = e.Clone
						break
					}
				}
			}

			cloneURL := revmap[p].CloneURL
			if !clone {
				cloneURL = ""
			}

			repoService := fs.service.NewRepoService(revmap[p].Name)

			opts := GitilesOptions{
				Revision:    revmap[p].Revision,
				CloneURL:    cloneURL,
				CloneOption: fs.options.FileCloneOption,
			}

			subRoot := NewGitilesRoot(fs.cache, fs.trees[p], repoService, opts)
			subRoot.(*gitilesRoot).nodeCache = fs.nodeCache
			parent.NewChild(base, true, subRoot)
			if err := subRoot.(*gitilesRoot).onMount(fsConn); err != nil {
				return fmt.Errorf("onMount(%s): %v", p, err)
			}
		}
	}

	// Do Linkfile, Copyfile after setting up the repos, so we
	// have directories to attach the copy/link nodes to.
	for _, p := range fs.options.Manifest.Project {
		for _, cp := range p.Copyfile {
			srcNode, left := fsConn.Node(fs.Inode(), filepath.Join(p.Path, cp.Src))
			if len(left) > 0 {
				return fmt.Errorf("Copyfile(%s): source %s does not exist", p.Name, cp.Src)
			}

			dir, left := fsConn.Node(fs.Inode(), cp.Dest)
			switch len(left) {
			case 0:
				return fmt.Errorf("Copyfile(%s): dest %s already exists.", p.Name, cp.Dest)
			case 1:
			default:
				return fmt.Errorf("Copyfile(%s): directory for dest %s does not exist.", p.Name, cp.Dest)
			}

			dir.AddChild(left[0], srcNode)
		}

		for _, lf := range p.Linkfile {
			dir, left := fsConn.Node(fs.Inode(), lf.Dest)
			switch len(left) {
			case 0:
				return fmt.Errorf("Linkfile(%s): dest %s already exists.", p.Name, lf.Dest)
			case 1:
			default:
				return fmt.Errorf("Linkfile(%s): directory for dest %s does not exist.", p.Name, lf.Dest)
			}

			src := filepath.Join(p.Path, lf.Src)
			rel, err := filepath.Rel(filepath.Dir(lf.Dest), src)
			if err != nil {
				return err
			}

			node := newLinkNode(filepath.Join(rel))
			dir.NewChild(left[0], false, node)
		}
	}

	metaNode := fs.Inode().NewChild(".slothfs", true, newDirNode())
	metaNode.NewChild("manifest.xml", false, newDataNode(fs.manifestXML))

	var tree gitiles.Tree
	treeContent, err := json.Marshal(tree)
	if err != nil {
		log.Panicf("json.Marshal: %v", err)
	}
	metaNode.NewChild("tree.json", false, newDataNode(treeContent))

	return nil
}

func fetchTreeMap(c *cache.Cache, service *gitiles.Service, mf *manifest.Manifest) (map[string]*gitiles.Tree, error) {
	type resultT struct {
		path string
		resp *gitiles.Tree
		err  error
	}

	// Fetch all the trees in parallel.
	out := make(chan resultT, len(mf.Project))
	for _, p := range mf.Project {
		go func(p manifest.Project) {
			revID, err := git.NewOid(p.Revision)
			if err != nil {
				out <- resultT{p.Path, nil, err}
				return
			}

			tree, err := c.Tree.Get(revID)
			if err != nil {
				if repo := c.Git.OpenLocal(p.CloneURL); repo != nil {
					defer repo.Free()
					tree, err = cache.GetTree(repo, revID)
				}
			}

			if err != nil {
				repoService := service.NewRepoService(p.Name)

				tree, err = repoService.GetTree(p.Revision, "", true)
				if err == nil {
					if err := c.Tree.Add(revID, tree); err != nil {
						log.Printf("treeCache.Add: %v", err)
					}
				}
			}

			out <- resultT{p.Path, tree, err}
		}(p)
	}

	// drain goroutines
	var result []resultT
	for range mf.Project {
		r := <-out
		result = append(result, r)
	}

	resmap := map[string]*gitiles.Tree{}
	for _, r := range result {
		if r.err != nil {
			return nil, fmt.Errorf("Tree(%s): %v", r.path, r.err)
		}

		resmap[r.path] = r.resp
	}
	return resmap, nil
}
