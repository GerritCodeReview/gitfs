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

package main

import (
	"crypto/sha1"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/google/gitfs/manifest"
)

// TODO - tests.

func getManifestWorkspace(mount string, wantFile string, want *manifest.Manifest) (string, error) {
	fs, err := filepath.Glob(filepath.Join(mount, "config", "*"))
	if err != nil {
		return "", err
	}

	for _, f := range fs {
		mf, err := manifest.ParseFile(f)
		if err != nil {
			return "", err
		}

		if reflect.DeepEqual(mf, want) {
			return filepath.Join(mount, filepath.Base(f)), nil
		}
	}

	name := fmt.Sprintf("ws_%s", manifestFingerprint(want))
	cfg := filepath.Join(mount, "config", name)
	if err := os.Symlink(wantFile, cfg); err != nil {
		return "", err
	}

	return cfg, nil
}

func manifestFingerprint(mf *manifest.Manifest) string {
	xml, err := mf.MarshalXML()
	if err != nil {
		return "xxxxxxxx"
	}

	h := sha1.New()
	h.Write(xml)
	return fmt.Sprintf("%s", h.Sum(nil))[:8]
}

type repoTree struct {
	children   map[string]*repoTree
	checkedOut bool
}

func newRepoTree() *repoTree {
	return &repoTree{children: make(map[string]*repoTree)}
}

func (t *repoTree) checkoutCount() int {
	n := 0
	if t.checkedOut {
		n++
	}
	for _, ch := range t.children {
		n += ch.checkoutCount()
	}
	return n
}

func construct(dir string) *repoTree {
	root := newRepoTree()
	stat, err := os.Stat(filepath.Join(dir, ".git"))
	if err == nil && stat.IsDir() {
		root.checkedOut = true
	}

	entries, err := ioutil.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && e.Name() == ".git" {
			continue
		}

		if e.IsDir() {
			subdir := construct(filepath.Join(dir, e.Name()))
			root.children[e.Name()] = subdir
		}
	}

	return root
}

func createLinks(dir, roDir string, root *repoTree) error {
	if root.checkedOut {
		// This is a leaf: we're done.
		if len(root.children) == 0 {
			return nil
		}
	} else {
		_, err := os.Lstat(dir)
		if err != nil && root.checkoutCount() == 0 {
			// we can symlink all of the children, so
			// symlink the whole thing in one go.
			return os.Symlink(roDir, dir)
		}

		entries, err := ioutil.ReadDir(roDir)
		if err != nil {
			return err
		}

		for _, e := range entries {
			nm := e.Name()
			roChild := filepath.Join(roDir, nm)
			rwChild := filepath.Join(dir, nm)

			if ch := root.children[nm]; ch == nil {
				if err := os.Symlink(roChild, rwChild); err != nil {
					return err
				}
			} else {
				if err := createLinks(rwChild, roChild, ch); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func clearLinks(dir, mount string) error {
	return filepath.Walk(dir, func(n string, fi os.FileInfo, err error) error {
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(n)
			if err != nil {
				return err
			}
			if strings.HasPrefix(target, mount) {
				if err := os.Remove(n); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func main() {
	mount := flag.String("ro", "", "path to gitfs-multifs mount.")
	flag.Parse()

	dir := "."
	if len(flag.Args()) == 1 {
		dir = flag.Arg(0)
	} else if len(flag.Args()) > 1 {
		log.Fatal("too many arguments.")
	}

	if err := clearLinks(dir, *mount); err != nil {
		log.Fatal(err)
	}

	rt := construct(dir)

	if err := createLinks(dir, *mount, rt); err != nil {
		log.Fatal(err)
	}
}
