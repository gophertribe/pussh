package pussh

import (
	"context"
	"io"
	"os/exec"
	"strings"
)

// dockerTag tags a local docker image.
func dockerTag(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, "docker", "tag", src, dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &DockerError{Op: "tag", Output: string(out), Err: err}
	}
	return nil
}

// dockerPush pushes an image to a registry.
func dockerPush(ctx context.Context, ref, platform string, stdout, stderr io.Writer) error {
	args := []string{"push"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, ref)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return &DockerError{Op: "push", Err: err}
	}
	return nil
}

// dockerPull pulls a docker image locally.
func dockerPull(ctx context.Context, ref, platform string, stdout, stderr io.Writer) error {
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	args = append(args, ref)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return &DockerError{Op: "pull", Err: err}
	}
	return nil
}

// dockerRmi removes a local image tag (best-effort, errors silenced).
func dockerRmi(ctx context.Context, ref string) error {
	cmd := exec.CommandContext(ctx, "docker", "rmi", ref)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// dockerRm removes a local container (best-effort).
func dockerRm(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", name)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// dockerImageExists checks if a docker image exists locally.
func dockerImageExists(ctx context.Context, ref string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", ref)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// dockerManifestHasArch checks if a local manifest contains the given architecture.
func dockerManifestHasArch(ctx context.Context, ref, arch string) bool {
	cmd := exec.CommandContext(ctx, "docker", "manifest", "inspect", ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	needle := "\"architecture\": \"" + arch + "\""
	return strings.Contains(string(out), needle)
}

// isDockerDesktop returns true if the local Docker is Docker Desktop.
func isDockerDesktop(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "version")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Docker Desktop")
}

// archFromPlatform extracts architecture from a platform string (e.g., "linux/amd64" → "amd64").
func archFromPlatform(p string) string {
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return p
}
