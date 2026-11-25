package lifecycled_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/buildkite/lifecycled"
)

func TestFileHandlerRunsPowerShellScript(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args.txt")
	t.Setenv("HANDLER_OUT", out)

	run := func(t *testing.T, body string) error {
		t.Helper()
		script := filepath.Join(dir, "handler.ps1")
		if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(script)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		return lifecycled.NewFileHandler(f).Execute(context.Background(), "ec2:SPOT_INSTANCE_TERMINATION", "i-123", "2015-01-05T18:02:00Z")
	}

	t.Run("passes arguments", func(t *testing.T) {
		if err := run(t, `$args -join "|" | Set-Content -NoNewline -Path $env:HANDLER_OUT`); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		if want := "ec2:SPOT_INSTANCE_TERMINATION|i-123|2015-01-05T18:02:00Z"; string(got) != want {
			t.Fatalf("handler got args %q, want %q", got, want)
		}
	})

	t.Run("returns script exit code", func(t *testing.T) {
		err := run(t, `exit 3`)
		exitErr, ok := err.(*exec.ExitError)
		if !ok || exitErr.ExitCode() != 3 {
			t.Fatalf("Execute error = %v, want exit code 3", err)
		}
	})
}
