package localadmin

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*
var templates embed.FS

// Init writes only absent starter files. It never imports the installing
// user's credentials or enables interventions or service auto-start.
func Init(l installpath.Layout, account string) error {
	for _, path := range []string{l.ConfigDir, l.StateDir, l.LogDir, filepath.Join(l.ConfigDir, "monitors.yaml"), filepath.Join(l.ConfigDir, "runtime.yaml"), filepath.Join(l.ConfigDir, "auth.token")} {
		if path != "" {
			if err := rejectSymlinkAncestors(path); err != nil {
				return err
			}
		}
	}
	account, err := nativePrepare(l, account)
	if err != nil {
		return err
	}
	// Initialization can adjust ownership on native platforms. Hold the same
	// exclusive lock as the runtime before touching an existing store.
	if _, err = os.Lstat(filepath.Join(l.StateDir, "raft.db")); err == nil {
		lock, err := persistence.LockOffline(l.StateDir)
		if err != nil {
			return err
		}
		defer lock.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, dir := range []string{l.ConfigDir, l.StateDir, l.LogDir} {
		if dir != "" {
			if err = os.MkdirAll(dir, 0700); err != nil {
				return err
			}
		}
	}
	if err = nativeSecure(l, account); err != nil {
		return err
	}
	manifest, err := templates.ReadFile("templates/monitors.yaml")
	if err != nil {
		return err
	}
	runtimeData, err := yaml.Marshal(map[string]any{"storage": map[string]any{"mode": "raft", "directory": l.StateDir}, "history": map[string]any{"retention_days": 30}})
	if err != nil {
		return err
	}
	token := make([]byte, 32)
	if _, err = rand.Read(token); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"monitors.yaml": manifest, "runtime.yaml": runtimeData, "auth.token": []byte(hex.EncodeToString(token) + "\n")} {
		if info, e := os.Lstat(filepath.Join(l.ConfigDir, name)); e == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("configuration must be a regular file: %s", name)
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
		err = writeNew(filepath.Join(l.ConfigDir, name), data, 0600)
		if err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nativeSecure(l, account)
}

func rejectSymlinkAncestors(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed path must not traverse a symbolic link: %s", path)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func binaryPath(l installpath.Layout) string {
	name := "cpra"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(l.BinDir, name)
}

func serviceArgs(l installpath.Layout) []string {
	return []string{"-yaml", filepath.Join(l.ConfigDir, "monitors.yaml"), "-runtime-config", filepath.Join(l.ConfigDir, "runtime.yaml"), "-data-dir", l.StateDir, "-web.addr", "127.0.0.1:8060", "-web.auth-file", filepath.Join(l.ConfigDir, "auth.token"), "-allow-empty", "-shutdown-timeout", "45s"}
}

// Render returns the service definition without creating files or accounts.
func Render(l installpath.Layout, account string) (string, error) {
	if account == "" {
		account = "cpra"
		if runtime.GOOS == "darwin" {
			account = "_cpra"
		}
	}
	if strings.ContainsAny(account, "\r\n\x00\t ") {
		return "", fmt.Errorf("invalid service account")
	}
	args := append([]string{binaryPath(l)}, serviceArgs(l)...)
	if runtime.GOOS == "windows" {
		return strings.Join(args, "\n") + "\n", nil
	}
	if runtime.GOOS == "darwin" {
		var b strings.Builder
		b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>io.github.ziad-hsn.cpra</string>\n<key>ProgramArguments</key><array>\n")
		xmlString := func(s string) {
			b.WriteString("<string>")
			_ = xml.EscapeText(&b, []byte(s))
			b.WriteString("</string>\n")
		}
		for _, arg := range args {
			xmlString(arg)
		}
		b.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n<key>ThrottleInterval</key><integer>10</integer>\n<key>ExitTimeOut</key><integer>60</integer>\n<key>Umask</key><integer>63</integer>\n")
		if l.Scope == "system" {
			b.WriteString("<key>UserName</key>")
			xmlString(account)
		}
		// launchd log files require operator rotation; bounded unified logging is
		// preferred. The service's stdout/stderr use launchd's default handling.
		b.WriteString("</dict></plist>\n")
		return b.String(), nil
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = systemdQuote(arg)
	}
	t, err := template.ParseFS(templates, "templates/cpra.service.tmpl")
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	err = t.Execute(&b, struct {
		System             bool
		Account, ExecStart string
	}{l.Scope == "system", account, strings.Join(quoted, " ")})
	return b.String(), err
}

func systemdQuote(s string) string {
	// Percent specifiers and environment expansion are active even in quotes.
	s = strings.ReplaceAll(strings.ReplaceAll(s, "%", "%%"), "$", "$$")
	return strconv.Quote(s)
}
