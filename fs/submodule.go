package fs

import (
	"io/ioutil"
	"log"
	"os"
	"strings"

	git "github.com/libgit2/git2go"
)

// Returns a path => URL map.
func ParseSubmodules(content []byte) (map[string]string, error) {
	f, err := ioutil.TempFile("", "submodule")
	if err != nil {
		log.Fatal(err)
	}
	f.Write([]byte(content))
	defer os.Remove(f.Name())

	base, err := git.NewConfig()
	if err != nil {
		log.Fatal(err)
	}
	defer base.Free()

	cfg2, err := git.OpenOndisk(base, f.Name())
	if err != nil {
		log.Fatal(err)
	}
	defer cfg2.Free()

	iter, err := cfg2.NewIterator()
	if err != nil {
		log.Fatal(err)
	}
	defer iter.Free()

	type val struct {
		path, url string
	}

	result := map[string]*val{}
	for {
		e, err := iter.Next()
		if git.IsErrorCode(err, git.ErrIterOver) {
			break
		}
		if err != nil {
			log.Fatalf("next: %#v", err)
		}
		if !strings.HasPrefix(e.Name, "submodule.") {
			continue
		}

		name := strings.TrimPrefix(e.Name, "submodule.")
		isURL := false
		isPath := false
		if strings.HasSuffix(name, ".url") {
			name = strings.TrimSuffix(name, ".url")
			isURL = true
		} else if strings.HasSuffix(name, ".path") {
			name = strings.TrimSuffix(name, ".path")
			isPath = true
		}

		if isURL || isPath {
			v := result[name]
			if v == nil {
				v = &val{}
			}

			if isURL {
				v.url = e.Value
			} else if isPath {
				v.path = e.Value
			}

			result[name] = v
		}
	}

	final := map[string]string{}
	for _, v := range result {
		final[v.path] = v.url
	}
	return final, nil
}
