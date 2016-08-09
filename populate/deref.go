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
	"fmt"

	"github.com/google/slothfs/gitiles"
	"github.com/google/slothfs/manifest"

	git "github.com/libgit2/git2go"
)

// FetchManifest gets the default manifest file from a Gitiles server.
func FetchManifest(service *gitiles.Service, repo, branch string) (*manifest.Manifest, error) {
	project := service.NewRepoService(repo)

	// When checking this out, it's called "manifest.xml". Go figure.
	c, err := project.GetBlob(branch, "default.xml")
	if err != nil {
		return nil, err
	}
	mf, err := manifest.Parse(c)
	if err != nil {
		return nil, err
	}

	return mf, nil
}

// DerefManifest uses the Gitiles JSON interface to fill in
// Project.Revision in the given manifest.
func DerefManifest(service *gitiles.Service, mf *manifest.Manifest) error {
	branchSet := map[string]struct{}{}

	var todoProjects []int
	for i, p := range mf.Project {
		rev := mf.ProjectRevision(&p)
		if _, err := git.NewOid(rev); err == nil {
			continue
		}

		branchSet[rev] = struct{}{}
		todoProjects = append(todoProjects, i)
	}

	var branches []string
	for k := range branchSet {
		branches = append(branches, k)
	}

	repos, err := service.List(branches)
	if err != nil {
		return err
	}
	for _, i := range todoProjects {
		p := &mf.Project[i]

		proj, ok := repos[p.Name]
		if !ok {
			return fmt.Errorf("server list doesn't mention repo %s", p.Name)
		}

		p.CloneURL = proj.CloneURL

		branch := mf.ProjectRevision(p)
		commit, ok := proj.Branches[branch]
		if !ok {
			return fmt.Errorf("branch %q for repo %s not returned", branch, p.Name)
		}

		p.Revision = commit
	}
	return nil
}
