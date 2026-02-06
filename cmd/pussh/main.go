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

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/gophertribe/pussh"
	"github.com/spf13/cobra"
)

const version = "0.1.0"

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[0;33m"
	colorBlue   = "\033[0;34m"
	colorDim    = "\033[2m"
	colorReset  = "\033[0m"
)

const usageMessage = `Usage: docker pussh [OPTIONS] IMAGE[:TAG] [USER@]HOST[:PORT]

Upload a Docker image to a remote Docker daemon via SSH without an external registry.

Options:
  -h, --help              Show this help message.
  -i, --ssh-key path      Path to SSH private key for remote login (if not already added to SSH agent).
      --platform string   Push a specific platform for a multi-platform image (e.g., linux/amd64, linux/arm64).
                          Local Docker has to use containerd image store to support multi-platform images.
                          For cross-platform builds (e.g., macOS to Linux), build with --platform first:
                          docker build --platform linux/amd64 -t myimage:latest .
                          When used with --image-transfer-mode copy, also determines the unregistry image platform.
      --image-transfer-mode string   How to transfer the unregistry image to remote host (remote|copy, default: remote).
                          'remote': Pull unregistry image directly on remote host (requires internet access).
                          'copy': Pull unregistry image locally and transfer via SSH stream (for air-gapped environments).
                          With --platform, pulls and transfers only the target platform variant.
                          Only used if unregistry image is not already available on remote host.

Examples:
  docker pussh myimage:latest user@host
  docker pussh --platform linux/amd64 myimage:latest host
  docker pussh myimage:latest user@host:2222 -i ~/.ssh/id_ed25519
  docker pussh --image-transfer-mode copy myimage:latest user@host
  # Cross-platform: build for target platform first
  docker build --platform linux/amd64 -t myimage:latest .
  docker pussh --platform linux/amd64 myimage:latest user@host
  # Air-gapped with platform-specific unregistry image
  docker pussh --image-transfer-mode copy --platform linux/amd64 myimage:latest user@host`

func main() {
	// Docker CLI plugin metadata
	if len(os.Args) > 1 && os.Args[1] == "docker-cli-plugin-metadata" {
		fmt.Println(pussh.PluginMetadataJSON(version))
		return
	}
	// If invoked by Docker as a plugin, sometimes the first arg is the command name
	if len(os.Args) > 1 && os.Args[1] == "pussh" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
	}

	var (
		sshKey   string
		platform string
		transfer string = "remote"
		showVer  bool
		verbose  bool
	)

	rootCmd := &cobra.Command{
		Use:   "pussh",
		Short: "Upload image to remote Docker daemon via SSH",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVer {
				fmt.Printf("docker-pussh, version %s\n", version)
				return nil
			}

			// Positional validation
			if len(args) != 2 {
				return errors.New("IMAGE and HOST are required.\nRun 'docker pussh --help' for usage information.")
			}
			image := args[0]
			sshAddr := args[1]

			// Create colored logger
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			logger := slog.New(newColorHandler(os.Stdout, level))

			opts := pussh.RunnerOptions{
				Image:             image,
				SSHAddress:        sshAddr,
				SSHKeyPath:        sshKey,
				Platform:          platform,
				ImageTransferMode: transfer,
				Logger:            logger,
				Stdout:            os.Stdout,
				Stderr:            os.Stderr,
			}

			if err := pussh.Execute(context.Background(), opts); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, " %s✓%s %s\n", colorGreen, colorReset,
				pussh.SuccessMessage(image, sshAddr))
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Flags
	rootCmd.Flags().BoolVarP(&showVer, "version", "v", false, "Show version")
	rootCmd.Flags().BoolVar(&verbose, "verbose", false, "Enable verbose/debug output")
	rootCmd.Flags().StringVarP(&sshKey, "ssh-key", "i", "", "Path to SSH private key for remote login")
	rootCmd.Flags().StringVar(&platform, "platform", "", "Target platform (e.g., linux/amd64)")
	rootCmd.Flags().StringVar(&transfer, "image-transfer-mode", transfer, "Image transfer mode: remote|copy")

	// Custom help output to match bash script
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) { fmt.Println(usageMessage) })

	// Customize flag error to mimic bash message style
	rootCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		msg := err.Error()
		if strings.Contains(msg, "unknown flag:") {
			parts := strings.Split(msg, ":")
			if len(parts) >= 2 {
				unknown := strings.TrimSpace(parts[1])
				return fmt.Errorf("Unknown option: %s\nRun 'docker pussh --help' for usage information.", unknown)
			}
		}
		return err
	})

	if err := rootCmd.Execute(); err != nil {
		printError(err)
		os.Exit(1)
	}
}

// printError displays a structured error message with colors and context.
func printError(err error) {
	fmt.Fprintf(os.Stderr, "\n%sERROR:%s ", colorRed, colorReset)

	// Check for typed errors and format with context
	var sshErr *pussh.SSHError
	var dockerErr *pussh.DockerError

	switch {
	case errors.As(err, &sshErr):
		fmt.Fprintf(os.Stderr, "ssh %s", sshErr.Op)
		if sshErr.Err != nil {
			fmt.Fprintf(os.Stderr, ": %v", sshErr.Err)
		}
		fmt.Fprintln(os.Stderr)
		if sshErr.Command != "" {
			fmt.Fprintf(os.Stderr, "  %s→%s command: %s\n", colorDim, colorReset, sshErr.Command)
		}
		if sshErr.Output != "" {
			output := strings.TrimSpace(sshErr.Output)
			fmt.Fprintf(os.Stderr, "  %s→%s output:  %s\n", colorDim, colorReset, output)
		}

	case errors.As(err, &dockerErr):
		if dockerErr.Remote {
			fmt.Fprintf(os.Stderr, "remote docker %s", dockerErr.Op)
		} else {
			fmt.Fprintf(os.Stderr, "docker %s", dockerErr.Op)
		}
		if dockerErr.Err != nil {
			fmt.Fprintf(os.Stderr, ": %v", dockerErr.Err)
		}
		fmt.Fprintln(os.Stderr)
		if dockerErr.Output != "" {
			output := strings.TrimSpace(dockerErr.Output)
			fmt.Fprintf(os.Stderr, "  %s→%s output: %s\n", colorDim, colorReset, output)
		}

	default:
		fmt.Fprintf(os.Stderr, "%v\n", err)
	}

	// Print hints for common sentinel errors
	switch {
	case errors.Is(err, pussh.ErrNoDocker):
		fmt.Fprintf(os.Stderr, "  %s→%s hint: ensure Docker is installed on the remote host\n", colorDim, colorReset)
	case errors.Is(err, pussh.ErrDockerPermission):
		fmt.Fprintf(os.Stderr, "  %s→%s hint: ensure Docker is running and user has permissions (root or docker group)\n", colorDim, colorReset)
	case errors.Is(err, pussh.ErrSSHNotFound):
		fmt.Fprintf(os.Stderr, "  %s→%s hint: ensure OpenSSH client is installed\n", colorDim, colorReset)
	case errors.Is(err, pussh.ErrInvalidAddress):
		fmt.Fprintf(os.Stderr, "  %s→%s hint: expected format [USER@]HOST[:PORT]\n", colorDim, colorReset)
	}

	fmt.Fprintln(os.Stderr)
}

// --- Colored slog handler ---

// colorHandler is a slog.Handler that produces colored terminal output.
type colorHandler struct {
	level  slog.Level
	writer *os.File
	attrs  []slog.Attr
}

func newColorHandler(w *os.File, level slog.Level) *colorHandler {
	return &colorHandler{level: level, writer: w}
}

func (h *colorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *colorHandler) Handle(_ context.Context, r slog.Record) error {
	var prefix, color string

	switch {
	case r.Level >= slog.LevelError:
		prefix = "✘"
		color = colorRed
	case r.Level >= slog.LevelWarn:
		prefix = "!"
		color = colorYellow
	case r.Level >= slog.LevelInfo:
		prefix = "•"
		color = colorBlue
	default: // Debug
		prefix = "·"
		color = colorDim
	}

	// Build attribute string for key=value pairs
	var attrs strings.Builder
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&attrs, " %s%s=%v%s", colorDim, a.Key, a.Value, colorReset)
		return true
	})
	// Include pre-set attrs from WithAttrs
	for _, a := range h.attrs {
		fmt.Fprintf(&attrs, " %s%s=%v%s", colorDim, a.Key, a.Value, colorReset)
	}

	fmt.Fprintf(h.writer, " %s%s%s %s%s\n",
		color, prefix, colorReset, r.Message, attrs.String())

	return nil
}

func (h *colorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &colorHandler{
		level:  h.level,
		writer: h.writer,
		attrs:  append(h.attrs, attrs...),
	}
}

func (h *colorHandler) WithGroup(_ string) slog.Handler {
	return h // groups not used
}
