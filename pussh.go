// Copyright 2024-2025 The pussh contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.
//
// This work is a derivative of unregistry (https://github.com/psviderski/unregistry),
// Copyright 2024 Pasha Sviderski, which is also licensed under the Apache License,
// Version 2.0.

package pussh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	defaultUnregistryImage = "ghcr.io/psviderski/unregistry:latest"
)

// RunnerOptions defines inputs for the Runner.
type RunnerOptions struct {
	Image             string
	SSHAddress        string // [user@]host[:port]
	SSHKeyPath        string // optional
	Platform          string // optional (e.g., linux/amd64)
	ImageTransferMode string // "remote" | "copy"
	Logger            *slog.Logger

	// UnregistryImage overrides the default unregistry image reference.
	// If empty, defaults to ghcr.io/psviderski/unregistry:latest.
	UnregistryImage string

	// ForceImageTransfer forces transfer of unregistry image even if it exists on remote.
	ForceImageTransfer bool

	// Stdout and Stderr are used for streaming docker command output.
	// If nil, output is discarded. The CLI sets these to os.Stdout/os.Stderr.
	Stdout io.Writer
	Stderr io.Writer
}

// Runner orchestrates the push process.
type Runner struct {
	opts RunnerOptions
	log  *slog.Logger

	// ssh executor
	ssh Executor

	// remote docker state
	remoteSudo bool

	// unregistry container on remote
	unregistryContainer string
	unregistryPort      int

	// local forward state
	localForwardPort int

	// docker desktop tunnel
	ddContainer string
	ddPort      int
}

// NewRunner creates a Runner with the given options.
func NewRunner(opts RunnerOptions) *Runner {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	return &Runner{
		opts: opts,
		log:  logger,
	}
}

// Run executes the full push flow.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.validateInputs(); err != nil {
		return err
	}
	defer r.cleanup()

	// Connect SSH
	r.log.Info("connecting to remote host", "address", r.opts.SSHAddress)
	conn, err := Connect(ctx, SSHConfig{
		Address: r.opts.SSHAddress,
		KeyPath: r.opts.SSHKeyPath,
		Logger:  r.log,
	})
	if err != nil {
		return fmt.Errorf("connect to %s: %w", r.opts.SSHAddress, err)
	}
	r.ssh = conn
	r.log.Info("connected to remote host", "address", r.opts.SSHAddress)

	// Check remote docker
	if err := r.checkRemoteDocker(ctx); err != nil {
		return err
	}
	r.log.Info("remote docker verified", "sudo", r.remoteSudo)

	// Start unregistry on remote
	if err := r.runUnregistry(ctx); err != nil {
		return err
	}
	r.log.Info("unregistry started on remote host",
		"port", r.unregistryPort, "container", r.unregistryContainer)

	// Forward port from local to remote unregistry
	localPort, err := r.forwardPort(ctx, r.unregistryPort)
	if err != nil {
		return err
	}
	r.localForwardPort = localPort
	r.log.Info("port forwarded",
		"local_port", localPort, "remote_port", r.unregistryPort)

	r.log.Debug("waiting for registry to be ready", "port", localPort)
	if err := waitForRegistry(ctx, localPort); err != nil {
		return fmt.Errorf("registry not ready: %w", err)
	}
	r.log.Debug("registry is ready", "port", localPort)

	pushPort := localPort

	// Docker Desktop handling
	if isDockerDesktop(ctx) {
		r.log.Info("Docker Desktop detected, creating tunnel",
			"host_port", localPort)
		p, name, err := r.runDockerDesktopTunnel(ctx, localPort)
		if err != nil {
			return err
		}
		r.ddPort = p
		r.ddContainer = name
		pushPort = p
		r.log.Info("Docker Desktop tunnel created",
			"tunnel_port", pushPort, "host_port", localPort)

		r.log.Debug("waiting for registry tunnel to be ready", "port", pushPort)
		if err := waitForRegistry(ctx, pushPort); err != nil {
			return fmt.Errorf("registry tunnel not ready: %w", err)
		}
		r.log.Debug("registry tunnel is ready", "port", pushPort)
	}

	// Tag and push
	registryImage := fmt.Sprintf("localhost:%d/%s", pushPort, r.opts.Image)
	if err := dockerTag(ctx, r.opts.Image, registryImage); err != nil {
		return fmt.Errorf("tag image for registry: %w", err)
	}
	defer func() { _ = dockerRmi(context.Background(), registryImage) }()

	r.log.Info("pushing image to unregistry", "image", registryImage)
	if err := dockerPush(ctx, registryImage, r.opts.Platform, r.stdout(), r.stderr()); err != nil {
		return fmt.Errorf("push image: %w", err)
	}
	r.log.Info("image pushed to unregistry", "image", registryImage)

	// If remote does not use containerd image store, pull and re-tag there
	usesContainerd, _ := r.remoteUsesContainerd(ctx)
	if !usesContainerd {
		r.log.Info("remote Docker does not use containerd image store, pulling from unregistry")
		remoteRegistryImage := fmt.Sprintf("localhost:%d/%s", r.unregistryPort, r.opts.Image)
		if err := r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("pull %s", shellQuote(remoteRegistryImage)))); err != nil {
			return fmt.Errorf("pull on remote: %w", err)
		}
		if err := r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("tag %s %s", shellQuote(remoteRegistryImage), shellQuote(r.opts.Image)))); err != nil {
			return fmt.Errorf("retag on remote: %w", err)
		}
		_ = r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("rmi %s", shellQuote(remoteRegistryImage))))
	}

	// Stop unregistry container on remote (best-effort)
	r.log.Info("removing unregistry container on remote host")
	_ = r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("rm -f %s", shellQuote(r.unregistryContainer))))

	return nil
}

func (r *Runner) validateInputs() error {
	if strings.TrimSpace(r.opts.Image) == "" || strings.TrimSpace(r.opts.SSHAddress) == "" {
		return errors.New("IMAGE and HOST are required")
	}
	if r.opts.ImageTransferMode == "" {
		r.opts.ImageTransferMode = "remote"
	}
	if r.opts.ImageTransferMode == "copy" {
		r.opts.ImageTransferMode = "scp"
	}
	if r.opts.ImageTransferMode != "remote" && r.opts.ImageTransferMode != "scp" {
		return errors.New("--image-transfer-mode must be either 'remote' or 'copy'")
	}
	if r.opts.SSHKeyPath != "" {
		if _, err := os.Stat(r.opts.SSHKeyPath); err != nil {
			return fmt.Errorf("SSH key file not found: %s", r.opts.SSHKeyPath)
		}
	}
	return nil
}

func (r *Runner) unregistryImage() string {
	if r.opts.UnregistryImage != "" {
		return r.opts.UnregistryImage
	}
	return defaultUnregistryImage
}

func (r *Runner) stdout() io.Writer {
	if r.opts.Stdout != nil {
		return r.opts.Stdout
	}
	return io.Discard
}

func (r *Runner) stderr() io.Writer {
	if r.opts.Stderr != nil {
		return r.opts.Stderr
	}
	return io.Discard
}

func (r *Runner) checkRemoteDocker(ctx context.Context) error {
	// Check if docker command is available
	if err := r.ssh.Run(ctx, "command -v docker >/dev/null 2>&1"); err != nil {
		return ErrNoDocker
	}
	// Try docker version without sudo
	if err := r.ssh.Run(ctx, "docker version >/dev/null 2>&1"); err == nil {
		r.remoteSudo = false
		return nil
	}
	// Try sudo docker version
	if err := r.ssh.Run(ctx, "[ $(id -u) -ne 0 ] && sudo -n docker version >/dev/null 2>&1"); err == nil {
		r.remoteSudo = true
		return nil
	}
	return ErrDockerPermission
}

func (r *Runner) runUnregistry(ctx context.Context) error {
	image := r.unregistryImage()

	needsTransfer := r.opts.ForceImageTransfer
	if !needsTransfer {
		if err := r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("image inspect %s >/dev/null 2>&1", shellQuote(image)))); err != nil {
			needsTransfer = true
		}
	}

	if needsTransfer {
		switch r.opts.ImageTransferMode {
		case "scp":
			r.log.Info("transferring unregistry image to remote host", "image", image, "forced", r.opts.ForceImageTransfer)
			if err := r.transferUnregistryImage(ctx); err != nil {
				return fmt.Errorf("transfer unregistry image: %w", err)
			}
		default:
			r.log.Info("pulling unregistry image on remote host", "image", image)
			if err := r.ssh.RunStreaming(ctx, r.dockerCmd(fmt.Sprintf("pull %s", shellQuote(image))), r.stdout(), r.stderr()); err != nil {
				return fmt.Errorf("pull unregistry image on remote: %w", err)
			}
		}
	}

	// Start container with retry on port bind conflict
	var lastErr error
	pid := os.Getpid()
	for i := 0; i < 10; i++ {
		port := randomPort()
		name := fmt.Sprintf("unregistry-pussh-%d-%d", pid, port)
		cmd := r.dockerCmd(fmt.Sprintf(
			"run -d --name %s -p 127.0.0.1:%d:5000 -v /run/containerd/containerd.sock:/run/containerd/containerd.sock --userns=host --user root:root %s",
			shellQuote(name), port, shellQuote(image),
		))
		out, err := r.ssh.RunOutput(ctx, cmd)
		if err == nil {
			r.unregistryPort = port
			r.unregistryContainer = name
			return nil
		}

		// Remove possibly created container
		_ = r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("rm -f %s", shellQuote(name))))

		// Check if error is due to port bind conflict
		errStr := strings.ToLower(fmt.Sprintf("%v %s", err, string(out)))
		if !strings.Contains(errStr, "bind") || !strings.Contains(errStr, fmt.Sprintf("%d", port)) {
			lastErr = &DockerError{
				Op:     "run",
				Remote: true,
				Output: string(out),
				Err:    fmt.Errorf("failed to start unregistry container: %w", err),
			}
			break
		}
		lastErr = fmt.Errorf("port conflict on %d", port)
	}
	if lastErr == nil {
		lastErr = ErrPortExhausted
	}
	return lastErr
}

func (r *Runner) remoteUsesContainerd(ctx context.Context) (bool, error) {
	out, err := r.ssh.RunOutput(ctx, r.dockerCmd("info -f '{{ .DriverStatus }}'"))
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), "containerd.snapshotter"), nil
}

func (r *Runner) dockerCmd(cmd string) string {
	if r.remoteSudo {
		return fmt.Sprintf("sudo docker %s", cmd)
	}
	return fmt.Sprintf("docker %s", cmd)
}

// forwardPort finds an available local port and establishes SSH port forwarding.
func (r *Runner) forwardPort(ctx context.Context, remotePort int) (int, error) {
	for i := 0; i < 10; i++ {
		localPort := randomPort()
		if err := r.ssh.Forward(ctx, localPort, remotePort); err == nil {
			return localPort, nil
		}
	}
	return 0, ErrPortExhausted
}

// runDockerDesktopTunnel starts a socat container to tunnel traffic from Docker Desktop VM to host port.
func (r *Runner) runDockerDesktopTunnel(ctx context.Context, hostPort int) (int, string, error) {
	name := fmt.Sprintf("docker-pussh-tunnel-%d", os.Getpid())
	var lastErr error
	for i := 0; i < 10; i++ {
		p := randomPort()
		args := []string{
			"run", "-d", "--rm",
			"--name", name,
			"-p", fmt.Sprintf("127.0.0.1:%d:5000", p),
			"alpine/socat",
			"TCP-LISTEN:5000,fork,reuseaddr",
			fmt.Sprintf("TCP-CONNECT:host.docker.internal:%d", hostPort),
		}
		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return p, name, nil
		}
		_ = dockerRm(ctx, name)
		lower := strings.ToLower(string(out))
		if !strings.Contains(lower, "bind") || !strings.Contains(lower, fmt.Sprintf("%d", p)) {
			lastErr = &DockerError{
				Op:     "run",
				Output: string(out),
				Err:    fmt.Errorf("failed to create Docker Desktop tunnel"),
			}
			break
		}
		lastErr = fmt.Errorf("port conflict for tunnel on %d", p)
	}
	if lastErr == nil {
		lastErr = ErrPortExhausted
	}
	return 0, "", lastErr
}

// cleanup releases resources best-effort.
func (r *Runner) cleanup() {
	ctx := context.Background()
	if r.ddContainer != "" {
		_ = dockerRm(ctx, r.ddContainer)
	}
	if r.localForwardPort != 0 && r.unregistryPort != 0 && r.ssh != nil {
		_ = r.ssh.CancelForward(ctx, r.localForwardPort, r.unregistryPort)
	}
	if r.unregistryContainer != "" && r.ssh != nil {
		_ = r.ssh.Run(ctx, r.dockerCmd(fmt.Sprintf("rm -f %s", shellQuote(r.unregistryContainer))))
	}
	if r.ssh != nil {
		_ = r.ssh.Close()
	}
}

// Execute is the simple entry point for callers.
func Execute(ctx context.Context, opts RunnerOptions) error {
	r := NewRunner(opts)
	return r.Run(ctx)
}

// PluginMetadataJSON returns Docker CLI plugin metadata JSON.
func PluginMetadataJSON(version string) string {
	return fmt.Sprintf(`{
  "SchemaVersion": "0.1.0",
  "Vendor": "https://github.com/gophertribe",
  "Version": "%s",
  "ShortDescription": "Upload image to remote Docker daemon via SSH without external registry"
}`, version)
}

// SuccessMessage returns a human-friendly success message.
func SuccessMessage(image, sshAddr string) string {
	return fmt.Sprintf("Successfully pushed '%s' to %s", image, sshAddr)
}

// --- Utilities ---

// randomPort returns a port in range 55000-65535.
func randomPort() int {
	return 55000 + rand.Intn(10536)
}

// parseSSHAddress parses [user@]host[:port].
func parseSSHAddress(addr string) (user, host, port string, err error) {
	re := regexp.MustCompile(`^([^@:]+@)?([^:]+)(:([0-9]+))?$`)
	m := re.FindStringSubmatch(addr)
	if m == nil {
		return "", "", "", ErrInvalidAddress
	}
	if m[1] != "" {
		user = strings.TrimSuffix(m[1], "@")
	}
	host = m[2]
	if m[4] != "" {
		port = m[4]
	} else {
		port = "22"
	}
	return
}

// shellQuote quotes a string for shell-safe usage.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexByte(s, '\'') == -1 {
		return "'" + s + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
