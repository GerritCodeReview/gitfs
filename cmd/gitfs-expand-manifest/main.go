package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/google/gitfs/gitiles"
	"github.com/google/gitfs/manifest"
)

func main() {
	gitilesURL := flag.String("gitiles", "", "URL for gitiles")
	branch := flag.String("branch", "master", "branch to use for manifest")
	repo := flag.String("repo", "platform/manifest", "manifest repository")
	flag.Parse()

	if *gitilesURL == "" {
		log.Fatal("must set --gitiles")
	}

	service, err := gitiles.NewService(*gitilesURL)
	if err != nil {
		log.Fatal(err)
	}

	mf, err := fetchManifest(service, *repo, *branch)
	if err != nil {
		log.Fatal(err)
	}

	filterManifest(mf)

	if err := derefManifest(service, *repo, mf); err != nil {
		log.Fatal(err)
	}

	if err := setCloneURLs(service, mf); err != nil {
		log.Fatal(err)
	}

	xml, err := mf.MarshalXML()
	if err != nil {
		log.Fatal(err)
	}

	os.Stdout.Write(xml)
}

func fetchManifest(service *gitiles.Service, repo, branch string) (*manifest.Manifest, error) {
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

func filterManifest(mf *manifest.Manifest) {
	filtered := *mf
	filtered.Project = nil
	for _, p := range mf.Project {
		if p.Groups["notdefault"] {
			continue
		}
		filtered.Project = append(filtered.Project, p)
	}
	*mf = filtered
}

func derefManifest(service *gitiles.Service, manifestRepo string, mf *manifest.Manifest) error {
	type resultT struct {
		i    int
		resp *gitiles.Commit
		err  error
	}
	out := make(chan resultT, len(mf.Project))

	// TODO(hanwen): avoid roundtrips if Revision is already a SHA1
	for i := range mf.Project {
		go func(i int) {
			p := mf.Project[i]
			repo := service.NewRepoService(p.Name)
			resp, err := repo.GetCommit(mf.ProjectRevision(&p))
			out <- resultT{i, resp, err}
		}(i)
	}

	for range mf.Project {
		r := <-out
		if r.err != nil {
			return r.err
		}
		mf.Project[r.i].Revision = r.resp.Commit
	}

	return nil
}

func setCloneURLs(service *gitiles.Service, mf *manifest.Manifest) error {
	repos, err := service.List()
	if err != nil {
		return err
	}

	for i, p := range mf.Project {
		proj, ok := repos[p.Name]
		if !ok {
			return fmt.Errorf("server list doesn't mention repo %s", p.Name)
		}

		mf.Project[i].CloneURL = proj.CloneURL
	}

	return nil
}
