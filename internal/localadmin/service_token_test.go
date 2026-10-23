package localadmin

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestServiceTokenReaderPolicy(t *testing.T) {
	l := testLayout(t)
	args := []string{"-web.auth-file", "/configured/token"}
	user, err := serviceTokenPolicy(l, "", args)
	if err != nil || !slices.Equal(user, args) {
		t.Fatal("user policy changed", err)
	}
	l.Scope = "system"
	managed, err := serviceTokenPolicy(l, "cpra", []string{"-runtime-config", "/configured/runtime"})
	if err != nil || slices.Contains(managed, "-web.auth-reader-group") || slices.Contains(managed, "-web.auth-reader-sid") {
		t.Fatal("management introduced bootstrap token", err)
	}
	account := "cpra"
	if runtime.GOOS == "darwin" {
		value, err := exec.Command("/usr/bin/id", "-un").Output()
		if err != nil {
			t.Fatal(err)
		}
		account = strings.TrimSpace(string(value))
	}
	system, err := serviceTokenPolicy(l, account, args)
	if err != nil || !slices.Contains(system, "-web.auth-root") {
		t.Fatal("system root policy", err)
	}
	flag := "-web.auth-reader-group"
	if runtime.GOOS == "windows" {
		flag = "-web.auth-reader-sid"
	}
	if !slices.Contains(system, flag) {
		t.Fatal("system token policy missing", system)
	}
	if runtime.GOOS == "darwin" {
		value, err := exec.Command("/usr/bin/id", "-g", account).Output()
		if err != nil {
			t.Fatal(err)
		}
		index := slices.Index(system, flag)
		if system[index+1] != strings.TrimSpace(string(value)) {
			t.Fatal("service policy did not use native numeric group", system)
		}
	}
}
