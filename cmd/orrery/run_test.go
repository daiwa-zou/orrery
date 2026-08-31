package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr is captureStdout's twin: run reports a configuration it cannot
// load on stderr, and the test has to read it there to tell that refusal apart
// from a silent one.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	f()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestRunVersionPrintsAndSucceeds(t *testing.T) {
	var code int
	out := captureStdout(t, func() { code = run([]string{"-version"}) })

	if code != 0 {
		t.Errorf("run(-version) = %d, want 0", code)
	}
	if !strings.Contains(out, "orrery "+version) {
		t.Errorf("run(-version) printed %q, want it to name the version %q", out, version)
	}
}

// The version is asked for before anything else is loaded, so `orrery
// -version` answers even on a host whose configuration is broken — which is
// the state someone asking a binary what it is tends to be in.
func TestRunVersionDoesNotNeedAConfig(t *testing.T) {
	t.Setenv("ORRERY_CONFIG", filepath.Join(t.TempDir(), "does-not-exist.yaml"))

	var code int
	_ = captureStdout(t, func() { code = run([]string{"-version"}) })

	if code != 0 {
		t.Errorf("run(-version) = %d with an unreadable config, want 0", code)
	}
}

// 2 is "your configuration is wrong", and it has to be distinguishable from
// the 1 a cluster it could not reach produces: one is fixed by editing a file,
// the other by looking at the network, and a supervisor that cannot tell them
// apart restarts forever on the first.
func TestRunRejectsAnUnreadableConfigWithCodeTwo(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")

	var code int
	errOut := captureStderr(t, func() { code = run([]string{"-config", missing}) })

	if code != 2 {
		t.Errorf("run with a missing config = %d, want 2", code)
	}
	if !strings.Contains(errOut, "configuration error") {
		t.Errorf("stderr = %q, want it to say what went wrong", errOut)
	}
	if !strings.Contains(errOut, missing) {
		t.Errorf("stderr = %q, want it to name the file it could not read", errOut)
	}
}

func TestRunRejectsInvalidYAMLWithCodeTwo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(path, []byte("server: [this is not a mapping\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var code int
	errOut := captureStderr(t, func() { code = run([]string{"-config", path}) })

	if code != 2 {
		t.Errorf("run with unparseable YAML = %d, want 2", code)
	}
	if !strings.Contains(errOut, "configuration error") {
		t.Errorf("stderr = %q, want it to say what went wrong", errOut)
	}
}

// -print-config is a debugging command and must not start serving; reaching
// server.New here would try to dial a cluster.
func TestRunPrintConfigPrintsAndStops(t *testing.T) {
	t.Setenv("ORRERY_CONFIG", "")

	var code int
	out := captureStdout(t, func() { code = run([]string{"-print-config"}) })

	if code != 0 {
		t.Errorf("run(-print-config) = %d, want 0", code)
	}
	for _, want := range []string{"server.addr:", "session.store:", "clusters:"} {
		if !strings.Contains(out, want) {
			t.Errorf("run(-print-config) printed %q, missing %q", out, want)
		}
	}
}

// -print-config reads the file it is given, which is the whole point of it:
// the question it answers is what this configuration resolves to.
func TestRunPrintConfigReadsTheNamedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orrery.yaml")
	body := "server:\n  addr: ':9999'\nclusters:\n  - name: only\n    kubeconfig: /etc/kube.yaml\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	var code int
	out := captureStdout(t, func() { code = run([]string{"-print-config", "-config", path}) })

	if code != 0 {
		t.Fatalf("run(-print-config -config %s) = %d, want 0", path, code)
	}
	if !strings.Contains(out, ":9999") {
		t.Errorf("the configured address is missing from:\n%s", out)
	}
	if !strings.Contains(out, "only") {
		t.Errorf("the configured cluster is missing from:\n%s", out)
	}
}

// ORRERY_CONFIG is the deployment's way of pointing at a file, so it has to
// have the same effect as passing -config.
func TestRunReadsTheConfigPathFromTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "orrery.yaml")
	if err := os.WriteFile(path, []byte("server:\n  addr: ':7777'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORRERY_CONFIG", path)

	var code int
	out := captureStdout(t, func() { code = run([]string{"-print-config"}) })

	if code != 0 {
		t.Fatalf("run(-print-config) = %d, want 0", code)
	}
	if !strings.Contains(out, ":7777") {
		t.Errorf("ORRERY_CONFIG was not read; output was:\n%s", out)
	}
}

// The two codes flag parsing produces, kept as the standard library's
// ExitOnError would have left them: a misspelt flag is a failure, and asking
// for help is not.
func TestRunFlagParsing(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"unknown flag", []string{"-nonesuch"}, 2},
		{"help", []string{"-h"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			var code int
			// Both write usage to stderr; swallow it so the test output is
			// only failures.
			_ = captureStderr(t, func() { code = run(c.args) })
			if code != c.want {
				t.Errorf("run(%v) = %d, want %d", c.args, code, c.want)
			}
		})
	}
}
