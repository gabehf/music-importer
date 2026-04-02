package main

import (
	"os"
	"os/exec"
)

// runCmd executes a shell command, forwarding stdout and stderr to the process output.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
