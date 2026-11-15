package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestPublicExposureRefusesTestIssuers(t *testing.T) {
	b, err := os.ReadFile("../../../deploy/workbench.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	test := "publicExposure: true\n" + strings.Replace(string(b), "issuers:\n", "issuers:\n    - {kind: company, url: https://smoke.workbench.test, audiences: [workbench], jwksFile: /keys.json}\n", 1)
	_ = os.WriteFile(p, []byte(test), 0o600)
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "smoke.workbench.test") {
		t.Fatalf("test issuer accepted on a public cluster: %v", err)
	}
	_ = os.WriteFile(p, []byte("publicExposure: true\n"+string(b)), 0o600)
	if _, err := Load(p); err != nil {
		t.Fatalf("real issuers refused: %v", err)
	}
}
