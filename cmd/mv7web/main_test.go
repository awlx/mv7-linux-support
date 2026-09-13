//go:build linux || darwin

package main

import (
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLauncherHelper(t *testing.T) {
	if os.Getenv("MV7WEB_TEST_HELPER") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("mv7web", flag.ExitOnError)
	os.Args = []string{"mv7web", "-addr", os.Getenv("MV7WEB_TEST_ADDR")}
	if os.Getenv("MV7WEB_TEST_OPEN") == "1" {
		os.Args = append(os.Args, "-open")
	}
	main()
}

func TestRunningDaemonLauncher(t *testing.T) {
	for _, open := range []bool{true, false} {
		name := "normal startup reports occupied address"
		if open {
			name = "web shortcut reuses running daemon"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, "existing daemon")
			}))
			defer server.Close()
			directory := t.TempDir()
			browserLog := filepath.Join(directory, "opened-url")
			processLog := filepath.Join(directory, "process-scan")
			tools := map[string]string{
				"xdg-open": "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MV7WEB_TEST_BROWSER_LOG\"\n",
				// Never let a regression inspect or signal real user processes.
				"pgrep": "#!/bin/sh\nprintf 'called\\n' > \"$MV7WEB_TEST_PROCESS_LOG\"\n",
			}
			for tool, script := range tools {
				if err := os.WriteFile(filepath.Join(directory, tool), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestLauncherHelper$")
			cmd.WaitDelay = time.Second
			cmd.Env = append(os.Environ(),
				"PATH="+directory,
				"MV7WEB_TEST_HELPER=1",
				"MV7WEB_TEST_ADDR="+strings.TrimPrefix(server.URL, "http://"),
				"MV7WEB_TEST_OPEN=0",
				"MV7WEB_TEST_BROWSER_LOG="+browserLog,
				"MV7WEB_TEST_PROCESS_LOG="+processLog,
			)
			if open {
				cmd.Env = append(cmd.Env, "MV7WEB_TEST_OPEN=1")
			}
			output, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("launcher timed out: %s", output)
			}
			if open && err != nil {
				t.Fatalf("web shortcut failed: %v\n%s", err, output)
			}
			if !open && (err == nil || !strings.Contains(string(output), "cannot listen on")) {
				t.Fatalf("normal startup must report occupied port: %v\n%s", err, output)
			}
			if _, err := os.Stat(processLog); !os.IsNotExist(err) {
				t.Fatalf("launcher must not search for or terminate existing daemons: %v", err)
			}
			if open {
				deadline := time.Now().Add(2 * time.Second)
				for {
					data, err := os.ReadFile(browserLog)
					if err == nil && string(data) == server.URL+"\n" {
						break
					}
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					if time.Now().After(deadline) {
						t.Fatalf("browser did not receive existing daemon URL: %q, %v", data, err)
					}
					time.Sleep(10 * time.Millisecond)
				}
			} else if _, err := os.Stat(browserLog); !os.IsNotExist(err) {
				t.Fatalf("normal startup must not open a browser: %v", err)
			}
			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatalf("existing daemon was disrupted: %v", err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil || string(data) != "existing daemon" {
				t.Fatalf("existing daemon response changed: %q, %v", data, err)
			}
		})
	}
}
