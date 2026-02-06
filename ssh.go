package pussh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Executor abstracts remote command execution over SSH.
// Implementations must be safe for sequential use but need not be concurrent-safe.
type Executor interface {
	// Run executes a command on the remote host. Returns error on non-zero exit.
	Run(ctx context.Context, command string) error

	// RunOutput executes a command and returns combined stdout+stderr.
	RunOutput(ctx context.Context, command string) ([]byte, error)

	// RunStreaming executes a command, streaming stdout and stderr to provided writers.
	RunStreaming(ctx context.Context, command string, stdout, stderr io.Writer) error

	// Forward establishes local port forwarding: localhost:localPort → remote 127.0.0.1:remotePort.
	Forward(ctx context.Context, localPort, remotePort int) error

	// CancelForward cancels a previously established port forward.
	CancelForward(ctx context.Context, localPort, remotePort int) error

	// Pipe executes a remote command with stdin piped from the provided reader.
	Pipe(ctx context.Context, remoteCommand string, stdin io.Reader, stderr io.Writer) error

	// Close terminates the SSH connection and cleans up resources.
	Close() error
}

// SSHConfig holds configuration for establishing an SSH connection.
type SSHConfig struct {
	Address string // [user@]host[:port]
	KeyPath string // optional path to SSH private key
	Logger  *slog.Logger
}

// SSHConnection manages a ControlMaster SSH connection.
// It implements the Executor interface by invoking the ssh CLI binary.
type SSHConnection struct {
	target      string // user@host or just host
	port        string
	keyPath     string
	controlPath string
	logger      *slog.Logger
	connected   bool
}

// Connect establishes a ControlMaster SSH connection and returns an SSHConnection.
// The connection is reused by all subsequent operations via the control socket.
func Connect(ctx context.Context, cfg SSHConfig) (*SSHConnection, error) {
	// Verify ssh binary exists
	if _, err := exec.LookPath("ssh"); err != nil {
		return nil, &SSHError{Op: "connect", Err: ErrSSHNotFound}
	}

	user, host, port, err := parseSSHAddress(cfg.Address)
	if err != nil {
		return nil, &SSHError{Op: "connect", Err: err}
	}

	target := host
	if user != "" {
		target = user + "@" + host
	}

	controlPath := filepath.Join(os.TempDir(), fmt.Sprintf("pussh-%d-%d.sock", os.Getpid(), rand.Intn(100000)))

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	c := &SSHConnection{
		target:      target,
		port:        port,
		keyPath:     cfg.KeyPath,
		controlPath: controlPath,
		logger:      logger,
	}

	// Establish ControlMaster: ssh -fN backgrounds the connection
	args := c.controlArgs()
	args = append(args, "-fN", c.target)

	logger.Debug("establishing SSH ControlMaster connection",
		"target", target, "port", port, "control_path", controlPath)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &SSHError{
			Op:     "connect",
			Output: stderr.String(),
			Err:    fmt.Errorf("failed to establish SSH connection to %s: %w", target, err),
		}
	}

	c.connected = true
	logger.Debug("SSH ControlMaster connection established", "target", target)
	return c, nil
}

// controlArgs returns the common SSH options for ControlMaster reuse.
func (c *SSHConnection) controlArgs() []string {
	args := []string{
		"-o", "ControlMaster=auto",
		"-o", fmt.Sprintf("ControlPath=%s", c.controlPath),
		"-o", "ControlPersist=1m",
		"-o", "ConnectTimeout=15",
	}
	if c.port != "22" {
		args = append(args, "-p", c.port)
	}
	if c.keyPath != "" {
		args = append(args, "-i", c.keyPath)
	}
	return args
}

// cmdArgs returns SSH args with ControlPath and target, ready for command execution.
func (c *SSHConnection) cmdArgs() []string {
	args := c.controlArgs()
	args = append(args, c.target)
	return args
}

// Run executes a remote command via the ControlMaster connection.
func (c *SSHConnection) Run(ctx context.Context, command string) error {
	args := c.cmdArgs()
	args = append(args, command)
	c.logger.Debug("ssh exec", "command", truncate(command, 120))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return &SSHError{
			Op:      "run",
			Command: truncate(command, 200),
			Output:  stderr.String(),
			Err:     err,
		}
	}
	return nil
}

// RunOutput executes a remote command and returns combined stdout+stderr.
func (c *SSHConnection) RunOutput(ctx context.Context, command string) ([]byte, error) {
	args := c.cmdArgs()
	args = append(args, command)
	c.logger.Debug("ssh exec (capture)", "command", truncate(command, 120))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, &SSHError{
			Op:      "run",
			Command: truncate(command, 200),
			Output:  string(out),
			Err:     err,
		}
	}
	return out, nil
}

// RunStreaming executes a remote command, streaming output to the provided writers.
func (c *SSHConnection) RunStreaming(ctx context.Context, command string, stdout, stderr io.Writer) error {
	args := c.cmdArgs()
	args = append(args, command)
	c.logger.Debug("ssh exec (streaming)", "command", truncate(command, 120))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return &SSHError{
			Op:      "run",
			Command: truncate(command, 200),
			Err:     err,
		}
	}
	return nil
}

// Forward establishes local port forwarding via ControlMaster -O forward.
func (c *SSHConnection) Forward(ctx context.Context, localPort, remotePort int) error {
	spec := fmt.Sprintf("%d:127.0.0.1:%d", localPort, remotePort)
	args := c.controlArgs()
	args = append(args, "-O", "forward", "-L", spec, c.target)

	c.logger.Debug("ssh forward", "local_port", localPort, "remote_port", remotePort)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &SSHError{
			Op:      "forward",
			Command: fmt.Sprintf("-O forward -L %s", spec),
			Output:  string(out),
			Err:     err,
		}
	}
	return nil
}

// CancelForward cancels a previously established port forward.
func (c *SSHConnection) CancelForward(ctx context.Context, localPort, remotePort int) error {
	spec := fmt.Sprintf("%d:127.0.0.1:%d", localPort, remotePort)
	args := c.controlArgs()
	args = append(args, "-O", "cancel", "-L", spec, c.target)

	c.logger.Debug("ssh cancel forward", "local_port", localPort, "remote_port", remotePort)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	_ = cmd.Run() // best-effort
	return nil
}

// Pipe executes a remote command with stdin piped from the provided reader.
func (c *SSHConnection) Pipe(ctx context.Context, remoteCommand string, stdin io.Reader, stderr io.Writer) error {
	args := c.cmdArgs()
	args = append(args, remoteCommand)

	c.logger.Debug("ssh pipe", "command", truncate(remoteCommand, 120))

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = stdin
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return &SSHError{
			Op:      "pipe",
			Command: truncate(remoteCommand, 200),
			Err:     err,
		}
	}
	return nil
}

// Close terminates the ControlMaster connection and removes the socket file.
func (c *SSHConnection) Close() error {
	if !c.connected {
		return nil
	}

	c.logger.Debug("closing SSH ControlMaster connection", "target", c.target)

	args := c.controlArgs()
	args = append(args, "-O", "exit", c.target)

	cmd := exec.Command("ssh", args...)
	cmd.Stderr = io.Discard
	err := cmd.Run()
	c.connected = false

	// Best-effort remove socket file
	_ = os.Remove(c.controlPath)

	return err
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
