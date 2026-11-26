package frontend

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const clientID = "320764385214-8m5obtbshb9ia4qf14s69ar9pv5is1nl.apps.googleusercontent.com"

// entrypointRun is the outcome of one entrypoint execution.
type entrypointRun struct {
	err    error
	output string
	// html is the directory the page was rendered into.
	html string
	// nginxArgs is what the stand-in nginx was started with, or empty if it
	// never started.
	nginxArgs string
}

// runEntrypoint executes entrypoint.sh with only the given environment, a
// stand-in nginx, and temporary directories, so it needs no root and no nginx.
func runEntrypoint(t *testing.T, env map[string]string) entrypointRun {
	t.Helper()
	return runEntrypointWith(t, env, nil)
}

// runEntrypointWith is runEntrypoint with a hook that prepares the html
// directory before the entrypoint runs.
func runEntrypointWith(t *testing.T, env map[string]string, prepare func(html string)) entrypointRun {
	t.Helper()
	dir := t.TempDir()
	html := filepath.Join(dir, "html")
	if prepare != nil {
		prepare(html)
	}
	argsFile := filepath.Join(dir, "nginx-args")

	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeNginx := "#!/bin/sh\nprintf '%s' \"$*\" > " + argsFile + "\n"
	if err := os.WriteFile(filepath.Join(bin, "nginx"), []byte(fakeNginx), 0o755); err != nil { //nolint:gosec // an executable test stub
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "sh", "./entrypoint.sh")
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"SIGNUP_SRC_DIR=.",
		"SIGNUP_HTML_DIR=" + html,
	}
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output

	run := entrypointRun{html: html}
	run.err = cmd.Run()
	run.output = output.String()
	if args, err := os.ReadFile(argsFile); err == nil {
		run.nginxArgs = string(args)
	}
	return run
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// The entrypoint injects the public configuration by substituting empty
// attributes. If the page stopped carrying them exactly once, the SPA would
// silently start without a client ID.
func TestPageCarriesTheConfigPlaceholders(t *testing.T) {
	page := readFile(t, "index.html")
	for _, placeholder := range []string{`data-client-id=""`, `data-api-base=""`, `name="signup-config"`} {
		if got := strings.Count(page, placeholder); got != 1 {
			t.Errorf("index.html has %d occurrences of %s, want exactly 1", got, placeholder)
		}
	}
}

// The entrypoint only accepts API origins the page's Content-Security-Policy
// allows. If the policy's allowance changes, the entrypoint's rule must change
// with it, or it would accept origins the browser then blocks.
func TestEntrypointAcceptsOnlyWhatTheCSPAllows(t *testing.T) {
	page := readFile(t, "index.html")
	if !strings.Contains(page, "connect-src 'self' https://*.gcp-hcp.devshift.net ") {
		t.Fatal("the page's connect-src no longer allows https://*.gcp-hcp.devshift.net; update the SIGNUP_API_BASE rule in entrypoint.sh to match")
	}
}

func TestEntrypointInjectsThePublicConfiguration(t *testing.T) {
	run := runEntrypoint(t, map[string]string{
		"SIGNUP_CLIENT_ID": clientID,
		"SIGNUP_API_BASE":  "https://signup-api.int.gcp-hcp.devshift.net",
	})
	if run.err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", run.err, run.output)
	}

	page := readFile(t, filepath.Join(run.html, "index.html"))
	for _, want := range []string{
		`data-client-id="` + clientID + `"`,
		`data-api-base="https://signup-api.int.gcp-hcp.devshift.net"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("rendered page does not contain %s", want)
		}
	}
	for _, asset := range []string{"app.js", "styles.css"} {
		if got, want := readFile(t, filepath.Join(run.html, asset)), readFile(t, asset); got != want {
			t.Errorf("%s was not copied unchanged", asset)
		}
	}
	if run.nginxArgs != "-g daemon off;" {
		t.Errorf("nginx started with %q, want %q", run.nginxArgs, "-g daemon off;")
	}
}

func TestEntrypointDefaultsToTheSameOriginAPI(t *testing.T) {
	run := runEntrypoint(t, map[string]string{"SIGNUP_CLIENT_ID": clientID})
	if run.err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", run.err, run.output)
	}
	if page := readFile(t, filepath.Join(run.html, "index.html")); !strings.Contains(page, `data-api-base=""`) {
		t.Error("an unset SIGNUP_API_BASE must leave the API base empty (same origin)")
	}
}

// Some runtimes copy the image's directory contents into a new volume. If that
// includes a symlink such as the nginx package's default index.html, rendering
// onto it must replace the link, not write through it to a read-only location.
func TestEntrypointDoesNotWriteThroughSymlinks(t *testing.T) {
	protected := filepath.Join(t.TempDir(), "protected.html")
	if err := os.WriteFile(protected, []byte("do not modify"), 0o444); err != nil { //nolint:gosec // a read-only fixture
		t.Fatal(err)
	}
	run := runEntrypointWith(t, map[string]string{"SIGNUP_CLIENT_ID": clientID}, func(html string) {
		if err := os.MkdirAll(html, 0o755); err != nil { //nolint:gosec // a test directory
			t.Fatal(err)
		}
		for _, name := range []string{"index.html", "app.js", "styles.css"} {
			if err := os.Symlink(protected, filepath.Join(html, name)); err != nil {
				t.Fatal(err)
			}
		}
	})
	if run.err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", run.err, run.output)
	}

	if got := readFile(t, protected); got != "do not modify" {
		t.Errorf("the entrypoint wrote through a symlink: %q", got)
	}
	for _, name := range []string{"index.html", "app.js", "styles.css"} {
		info, err := os.Lstat(filepath.Join(run.html, name))
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() {
			t.Errorf("%s is %v, want a regular file", name, info.Mode())
		}
	}
}

func TestEntrypointRequiresAClientID(t *testing.T) {
	run := runEntrypoint(t, nil)
	if run.err == nil {
		t.Fatal("entrypoint started without SIGNUP_CLIENT_ID")
	}
	if !strings.Contains(run.output, "SIGNUP_CLIENT_ID") {
		t.Errorf("output does not name the missing setting: %q", run.output)
	}
	if run.nginxArgs != "" {
		t.Error("nginx started without a client ID")
	}
}

// Values go into an HTML attribute through sed, so anything that could end the
// attribute, change the sed expression, or point the SPA at an insecure or
// unexpected API must stop the container rather than be rendered.
func TestEntrypointRejectsUnsafeValues(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"client id closes the attribute", "SIGNUP_CLIENT_ID", `x" onmouseover="alert(1)`},
		{"client id with a sed delimiter", "SIGNUP_CLIENT_ID", "a|b"},
		{"client id with a sed backreference", "SIGNUP_CLIENT_ID", "a&b"},
		{"client id with markup", "SIGNUP_CLIENT_ID", "<script>"},
		{"client id with a space", "SIGNUP_CLIENT_ID", "a b"},
		{"api base over http", "SIGNUP_API_BASE", "http://signup.example.com"},
		{"api base without a scheme", "SIGNUP_API_BASE", "signup.example.com"},
		{"api base scheme-relative", "SIGNUP_API_BASE", "//evil.example.com"},
		{"api base with a path", "SIGNUP_API_BASE", "https://signup.int.gcp-hcp.devshift.net/path"},
		{"api base closes the attribute", "SIGNUP_API_BASE", `https://x.gcp-hcp.devshift.net"><script>`},
		{"api base outside the page's CSP", "SIGNUP_API_BASE", "https://signup.example.com"},
		{"api base on a lookalike domain", "SIGNUP_API_BASE", "https://signup.gcp-hcp.devshift.net.evil.example"},
		{"api base on the bare domain", "SIGNUP_API_BASE", "https://gcp-hcp.devshift.net"},
		{"api base with a port", "SIGNUP_API_BASE", "https://signup.int.gcp-hcp.devshift.net:8443"},
		{"api base with no host", "SIGNUP_API_BASE", "https://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"SIGNUP_CLIENT_ID": clientID}
			env[tc.key] = tc.value

			run := runEntrypoint(t, env)
			if run.err == nil {
				t.Fatalf("entrypoint accepted %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(run.output, tc.key) {
				t.Errorf("output does not name %s: %q", tc.key, run.output)
			}
			if run.nginxArgs != "" {
				t.Error("nginx started with an unsafe value")
			}
			if _, err := os.Stat(filepath.Join(run.html, "index.html")); err == nil {
				t.Error("a page was rendered with an unsafe value")
			}
		})
	}
}
