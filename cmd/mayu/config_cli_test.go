package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMayuConfigCLIProcess(t *testing.T) {
	if os.Getenv("MAYU_CONFIG_CLI_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"mayu"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func runConfigCLI(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestMayuConfigCLIProcess$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "MAYU_CONFIG_CLI_TEST_PROCESS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return string(out), exit.ExitCode()
	}
	t.Fatal(err)
	return "", -1
}

func TestServeEqualsConfigUsesRequestedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "selected-missing.json")
	out, code := runConfigCLI(t, dir, "serve", "--config="+path)
	if code != 1 || !strings.Contains(out, path) {
		t.Fatalf("requested config path must be honored: exit=%d output=%s", code, out)
	}
}

func TestServeRejectsUnknownFlags(t *testing.T) {
	out, code := runConfigCLI(t, t.TempDir(), "serve", "--confg=wrong.json")
	if code != 2 || !strings.Contains(out, "confg") {
		t.Fatalf("unknown flag must fail before reading another config: exit=%d output=%s", code, out)
	}
}

func TestConfigValidateAcceptsValidAndRejectsTypos(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, body string
		exit       int
	}{
		{"valid", `{}`, 0},
		{"pricing-typo", `{"pricing":{"on_misssing":"block"}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			out, code := runConfigCLI(t, dir, "config", "validate", "--config="+path)
			if code != tc.exit {
				t.Fatalf("validate exit=%d want=%d output=%s", code, tc.exit, out)
			}
		})
	}
}
