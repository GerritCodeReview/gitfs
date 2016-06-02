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

// Package gitiles is a client library for the Gitiles source viewer.
package gitiles

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
)

// Service is client for the Gitiles JSON interface.
type Service struct {
	throttle chan struct{}
	addr     url.URL
}

// Addr returns the address of the gitiles service.
func (s *Service) Addr() string {
	return s.addr.String()
}

// NewService returns a new Gitiles JSON client.
func NewService(addr string) (*Service, error) {
	url, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	return &Service{
		throttle: make(chan struct{}, 12),
		addr:     *url,
	}, nil
}

func (s *Service) getJSON(url string, dest interface{}) error {
	s.throttle <- struct{}{}
	resp, err := http.Get(url)
	<-s.throttle
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}

	c, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	i := bytes.IndexByte(c, '\n')
	if i < 0 {
		return fmt.Errorf("Gitiles JSON %s missing header, %q", url, c)
	}

	c = c[i+1:]

	err = json.Unmarshal(c, dest)
	if err != nil {
		err = fmt.Errorf("Unmarshal(%s): %v", url, err)
	}
	return err
}

// List retrieves the list of projects.
func (s *Service) List() (map[string]*Project, error) {
	listURL := s.addr
	listURL.RawQuery = "format=JSON"

	projects := map[string]*Project{}
	err := s.getJSON(listURL.String(), &projects)
	return projects, err
}

// GetProject retrieves a single project.
func (s *Service) GetProject(name string) (*Project, error) {
	jsonURL := s.addr
	jsonURL.Path = path.Join(s.addr.Path, name)
	jsonURL.RawQuery = "format=JSON"

	var p Project
	err := s.getJSON(jsonURL.String(), &p)
	return &p, err
}

// RepoService is a JSON client for the functionality of a specific
// respository.
type RepoService struct {
	Name    string
	service *Service
}

// GetBlob fetches a blob.
func (s *RepoService) GetBlob(branch, filename string) ([]byte, error) {
	blobURL := s.service.addr

	blobURL.Path = path.Join(blobURL.Path, s.Name, "+show", branch, filename)
	blobURL.RawQuery = "format=TEXT"

	// TODO(hanwen): invent a more structured mechanism for logging.
	log.Println(blobURL.String())
	s.service.throttle <- struct{}{}
	resp, err := http.Get(blobURL.String())
	<-s.service.throttle
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", blobURL.String(), resp.Status)
	}
	c, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	out := make([]byte, base64.StdEncoding.DecodedLen(len(c)))
	n, err := base64.StdEncoding.Decode(out, c)

	return out[:n], err
}

// GetTree fetches a tree. The dir argument may not point to a
// blob. If recursive is given, the server recursively expands the
// tree.
func (s *RepoService) GetTree(branch, dir string, recursive bool) (*Tree, error) {
	jsonURL := s.service.addr
	jsonURL.Path = path.Join(jsonURL.Path, s.Name, "+", branch, dir)
	if dir == "" {
		jsonURL.Path += "/"
	}
	jsonURL.RawQuery = "format=JSON&long=1"

	if recursive {
		jsonURL.RawQuery += "&recursive=1"
	}

	var tree Tree
	err := s.service.getJSON(jsonURL.String(), &tree)
	return &tree, err
}

// GetCommit gets the data of a commit in a branch.
func (s *RepoService) GetCommit(branch string) (*Commit, error) {
	jsonURL := s.service.addr
	jsonURL.Path = path.Join(jsonURL.Path, s.Name, "+", branch)
	jsonURL.RawQuery = "format=JSON"

	var c Commit
	err := s.service.getJSON(jsonURL.String(), &c)
	return &c, err
}
