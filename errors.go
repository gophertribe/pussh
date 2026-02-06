package pussh

import (
	"errors"
	"fmt"
	"strings"
)

// SSHError wraps SSH connection and execution failures.
type SSHError struct {
	Op      string // "connect", "run", "forward", "pipe", "close"
	Command string // the remote command or ssh subcommand
	Output  string // stderr output from ssh process
	Err     error
}

func (e *SSHError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "ssh %s", e.Op)
	if e.Command != "" {
		fmt.Fprintf(&b, " [%s]", e.Command)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	if e.Output != "" {
		fmt.Fprintf(&b, "\n  output: %s", strings.TrimSpace(e.Output))
	}
	return b.String()
}

func (e *SSHError) Unwrap() error { return e.Err }

// DockerError wraps docker CLI failures (local or remote).
type DockerError struct {
	Op     string // "tag", "push", "pull", "save", "load", "run", "inspect", "info"
	Remote bool   // true if this was a remote docker command
	Output string // stderr/combined output
	Err    error
}

func (e *DockerError) Error() string {
	var b strings.Builder
	if e.Remote {
		fmt.Fprintf(&b, "remote docker %s", e.Op)
	} else {
		fmt.Fprintf(&b, "docker %s", e.Op)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	if e.Output != "" {
		fmt.Fprintf(&b, "\n  output: %s", strings.TrimSpace(e.Output))
	}
	return b.String()
}

func (e *DockerError) Unwrap() error { return e.Err }

// Sentinel errors.
var (
	ErrNoDocker         = errors.New("docker command not found on remote host")
	ErrDockerPermission = errors.New("cannot run docker commands on remote host; ensure Docker is running and user has permissions (root or docker group)")
	ErrPortExhausted    = errors.New("no available port found after retries")
	ErrInvalidAddress   = errors.New("invalid SSH address format; expected [USER@]HOST[:PORT]")
	ErrSSHNotFound      = errors.New("ssh command not found in PATH")
)
