package localadmin

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestLaunchdStatusParser(t *testing.T) {
	s := parseLaunchdStatus("gui/501/io.github.ziad-hsn.cpra = {\n state = running\n pid = 1234\n}\n")
	if !s.loaded || !s.running || s.pid != 1234 {
		t.Fatal(s)
	}
	s = parseLaunchdStatus("state = waiting\nlast exit code = 1")
	if !s.loaded || s.running || s.pid != 0 {
		t.Fatal(s)
	}
}
func TestLaunchdArgumentsAreXMLData(t *testing.T) {
	l := testLayout(t)
	l.ConfigDir += " with <&> characters"
	value, err := Render(l, "")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(value))
	found := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if text, ok := token.(xml.CharData); ok && strings.Contains(string(text), "with <&> characters") {
			found = true
		}
	}
	if !found {
		t.Fatal("plist escaped arguments did not round trip")
	}
	if strings.Contains(value, "<key>UserName</key>") {
		t.Fatal("LaunchAgent changed identity")
	}
	l.Scope = "system"
	value, err = Render(l, "_cpra")
	if err != nil || !strings.Contains(value, "<key>UserName</key><string>_cpra</string>") {
		t.Fatalf("missing explicit daemon identity: %s %v", value, err)
	}
}
