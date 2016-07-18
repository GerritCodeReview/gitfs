package fs

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSubmodules(t *testing.T) {
	modules := `
[submodule "foo"]
	path = plugins/cookbook-plugin
	url = ../plugins/cookbook-plugin

[submodule "plugins/download-commands"]
	path = plugins/download-commands
	url = ../plugins/download-commands
`

	result, err := ParseSubmodules([]byte(modules))
	if err != nil {
		t.Fatalf("ParseSubmodules: %v", err)
	}

	want := map[string]string{
		"plugins/cookbook-plugin":   "../plugins/cookbook-plugin",
		"plugins/download-commands": "../plugins/download-commands",
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("got %v, want %v", result, want)
	}
}

func TestSubmoduleE2E(t *testing.T) {
	fix, err := newTestFixture()
	if err != nil {
		t.Fatal("newTestFixture", err)
	}
	defer fix.cleanup()

	ids := []string{
		"787d767f94fd634ed29cd69ec9f93bab2b25f5d4",
		"91c29720b08211898308eb2b6bde8bd3208c6dcd",
		"bdea84459e8c5266251248e593c8ba226a535ad2",
		"072b5fc6ca14a64f35f7841080e4b9c972c89b3d",
	}

	dir, err := ioutil.TempDir()

	repo := filepath.Join(dir, "gitrepo")

	cmd := exec.Command("/bin/sh", "-eux", "-c",
		"mkdir -p "+dir+`; git init ; echo hello > test ; git add test ; git commit -a -m msg test`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	repo, err := git.OpenRepository(repo + "/.git")

	repo.Lookup

	if err := fix.Cache.Add(ids[0], fmt.Sprintf(`
[submodule "sm"]
	path = sm
	url = file:///%s`, repo)); err != nil {
		t.Fatal(err)
	}

	gitiles.Tree

}
