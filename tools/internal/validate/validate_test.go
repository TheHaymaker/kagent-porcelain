package validate

import "testing"

func TestRepoPath(t *testing.T) {
	good := []string{"main.go", "src/app/index.ts", ".gitlab-ci.yml", "a/.env.example"}
	bad := []string{"", "/etc/passwd", "../x", "a/../b", "a//b", "a/./b", ".git/config", "a/", "a\\b", "a\x00b"}
	for _, p := range good {
		if err := RepoPath("p", p); err != nil {
			t.Errorf("%q: unexpected %v", p, err)
		}
	}
	for _, p := range bad {
		if err := RepoPath("p", p); err == nil {
			t.Errorf("%q: expected error", p)
		}
	}
}

func TestGitRefName(t *testing.T) {
	good := []string{"main", "feature/x", "wb/fix-1.2"}
	bad := []string{"", "-x", "a..b", "a/", "x.lock", "a b", "a@{1}", "a//b"}
	for _, p := range good {
		if err := GitRefName("b", p); err != nil {
			t.Errorf("%q: unexpected %v", p, err)
		}
	}
	for _, p := range bad {
		if err := GitRefName("b", p); err == nil {
			t.Errorf("%q: expected error", p)
		}
	}
}

func TestLabelValue(t *testing.T) {
	if got := LabelValue("Alice.Smith@Example.com"); got != "alice_smith_example_com" {
		t.Fatalf("got %q", got)
	}
	long := LabelValue(string(make([]byte, 100)))
	if len(long) != 63 {
		t.Fatalf("len %d", len(long))
	}
}

func TestBounded(t *testing.T) {
	if Bounded(0, 20, 100) != 20 || Bounded(500, 20, 100) != 100 || Bounded(5, 20, 100) != 5 {
		t.Fatal("Bounded wrong")
	}
}

func TestText(t *testing.T) {
	if Text("t", "ok\nline", true, 100) != nil {
		t.Fatal("newline should be fine")
	}
	if Text("t", "bad\x1b[31m", true, 100) == nil {
		t.Fatal("escape should be rejected")
	}
	if Text("t", "", true, 10) == nil || Text("t", "", false, 10) != nil {
		t.Fatal("required handling wrong")
	}
	if Text("t", "12345678901", false, 10) == nil {
		t.Fatal("length not enforced")
	}
}
