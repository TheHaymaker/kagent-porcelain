package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("../../../deploy/workbench.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Policy().Kinds) != 4 || c.Production.Deployer != "cloudrun" {
		t.Fatalf("%+v", c)
	}
}

func TestRejectsUnknownFieldsAndGaps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	_ = os.WriteFile(p, []byte("publicUrl: x\nmystery: 1\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("unknown field accepted")
	}
	_ = os.WriteFile(p, []byte("publicUrl: x\n"), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("empty config accepted")
	}
}
