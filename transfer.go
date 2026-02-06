package pussh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// transferUnregistryImage transfers the unregistry image to the remote host via SSH pipe.
// This is used in "copy" (air-gapped) mode: docker save | ssh docker load.
func (r *Runner) transferUnregistryImage(ctx context.Context) error {
	image := r.unregistryImage()

	// Ensure local image exists for the target platform
	if r.opts.Platform != "" {
		arch := archFromPlatform(r.opts.Platform)
		if !dockerImageExists(ctx, image) || !dockerManifestHasArch(ctx, image, arch) {
			r.log.Info("pulling unregistry image locally",
				"image", image, "platform", r.opts.Platform)
			if err := dockerPull(ctx, image, r.opts.Platform, io.Discard, io.Discard); err != nil {
				return fmt.Errorf("pull unregistry image for platform %s: %w", r.opts.Platform, err)
			}
		}
	} else {
		if !dockerImageExists(ctx, image) {
			r.log.Info("pulling unregistry image locally", "image", image)
			if err := dockerPull(ctx, image, "", io.Discard, io.Discard); err != nil {
				return fmt.Errorf("pull unregistry image: %w", err)
			}
		}
	}
	r.log.Info("unregistry image available locally", "image", image)

	// Build docker save command
	saveArgs := []string{"save"}
	if r.opts.Platform != "" {
		saveArgs = append(saveArgs, "--platform", r.opts.Platform)
	}
	saveArgs = append(saveArgs, image)

	saveCmd := exec.CommandContext(ctx, "docker", saveArgs...)
	stdout, err := saveCmd.StdoutPipe()
	if err != nil {
		return &DockerError{Op: "save", Err: err}
	}
	var saveStderr bytes.Buffer
	saveCmd.Stderr = &saveStderr

	if err := saveCmd.Start(); err != nil {
		return &DockerError{Op: "save", Err: err}
	}

	r.log.Info("transferring unregistry image to remote host via SSH",
		"image", image)

	// Pipe docker save stdout → ssh docker load
	var loadStderr bytes.Buffer
	remoteCmd := r.dockerCmd("load")

	pipeErrCh := make(chan error, 1)
	go func() {
		pipeErrCh <- r.ssh.Pipe(ctx, remoteCmd, stdout, &loadStderr)
	}()

	// Wait for local docker save to finish
	if err := saveCmd.Wait(); err != nil {
		return &DockerError{
			Op:     "save",
			Output: saveStderr.String(),
			Err:    err,
		}
	}

	// Wait for remote docker load to finish
	if err := <-pipeErrCh; err != nil {
		return &DockerError{
			Op:     "load",
			Remote: true,
			Output: loadStderr.String(),
			Err:    err,
		}
	}

	r.log.Info("unregistry image transferred to remote host")
	return nil
}
