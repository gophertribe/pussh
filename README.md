# pussh

Push Docker images from localhost to a remote machine via SSH without requiring a Docker registry.

## Overview

`pussh` is a Go library and Docker CLI plugin that enables seamless transfer of Docker images to remote hosts over SSH. It eliminates the need for external registries by leveraging the [unregistry](https://github.com/psviderski/unregistry) container on the remote host.

**Note**: This code is heavily inspired by the work of [psvidersky/unregistry](https://github.com/psviderski/unregistry). I rewrote initial bash implementation to Go to maintain greater control over the process and add a possibility to use it as a Go library as this was my main usecase.

## How It Works

1. **SSH Connection**: Establishes an SSH connection to the remote host
2. **Remote Docker Check**: Verifies Docker is installed and accessible on the remote host
3. **Unregistry Setup**: Starts an `unregistry` container on the remote host. Ensures it is available and pulls or copies it if not
4. **Port Forwarding**: Creates an SSH tunnel from localhost to the remote unregistry service
5. **Image Push**: Tags the local image and pushes it through the forwarded port
6. **Remote Import**: On the remote host, pulls the image from unregistry and retags it appropriately

The library automatically handles:

- Docker Desktop environments (creates additional tunnel)
- Remote Docker permission detection (sudo vs non-sudo)
- Containerd vs non-containerd image stores
- Multi-platform image support
- Air-gapped environments (image transfer via SSH stream)

## Installation

### As a Go Library

```bash
go get github.com/gophertribe/pussh
```

### As a Docker CLI Plugin

Build the plugin:

```bash
make build
```

Install the plugin:

```bash
make install-docker-plugin
```

This will copy the binary to `~/.docker/cli-plugins/docker-pussh`, making it available as `docker pussh`.

## Usage

### As a Go Library

```go
package main

import (
    "context"
    "log/slog"
    "os"
    
    "github.com/gophertribe/pussh"
)

func main() {
    ctx := context.Background()
    
    opts := pussh.RunnerOptions{
        Image:             "myimage:latest",
        SSHAddress:        "user@example.com",
        SSHKeyPath:        "/path/to/ssh/key", // optional
        Platform:          "linux/amd64",      // optional
        ImageTransferMode: "remote",            // "remote" or "copy"
        Logger:            slog.Default(),
        Stdout:            os.Stdout,
        Stderr:            os.Stderr,
    }
    
    if err := pussh.Execute(ctx, opts); err != nil {
        log.Fatal(err)
    }
}
```

#### Configuration Options

- **Image**: The Docker image reference to push (e.g., `myimage:latest`)
- **SSHAddress**: Remote host address in format `[user@]host[:port]` (e.g., `user@example.com:2222`)
- **SSHKeyPath**: Optional path to SSH private key (if not using SSH agent)
- **Platform**: Optional target platform (e.g., `linux/amd64`, `linux/arm64`) for multi-platform images
- **ImageTransferMode**: How to transfer the unregistry image to remote host:
  - `"remote"`: Pull unregistry image directly on remote host (requires internet access)
  - `"copy"`: Transfer unregistry image via SSH stream (for air-gapped environments)
- **Logger**: Optional structured logger (uses `slog.Default()` if nil)
- **Stdout/Stderr**: Optional writers for Docker command output (discarded if nil)
- **UnregistryImage**: Optional override for unregistry image (defaults to `ghcr.io/psviderski/unregistry:latest`)

### As a Docker CLI Plugin

Once installed, use it as a Docker plugin:

```bash
# Basic usage
docker pussh myimage:latest user@host

# With SSH key
docker pussh -i ~/.ssh/id_ed25519 myimage:latest user@host

# With custom SSH port
docker pussh myimage:latest user@host:2222

# Push specific platform from multi-platform image
docker pussh --platform linux/amd64 myimage:latest user@host

# Air-gapped mode (transfer unregistry image via SSH)
docker pussh --image-transfer-mode copy myimage:latest user@host

# Cross-platform build and push
docker build --platform linux/amd64 -t myimage:latest .
docker pussh --platform linux/amd64 myimage:latest user@host

# Verbose output
docker pussh --verbose myimage:latest user@host
```

#### CLI Options

- `-i, --ssh-key`: Path to SSH private key
- `--platform`: Target platform (e.g., `linux/amd64`)
- `--image-transfer-mode`: Image transfer mode (`remote` or `copy`, default: `remote`)
- `--verbose`: Enable verbose/debug output
- `-v, --version`: Show version

## Requirements

### Local Machine

- Docker installed and running
- OpenSSH client (`ssh` command available)
- Go 1.24+ (for building from source)

### Remote Machine

- Docker installed and running
- SSH access with appropriate permissions
- User must be able to run Docker commands (either as root, in docker group, or with sudo)

## Examples

### Basic Push

```go
import "github.com/gophertribe/pussh"

ctx := context.Background()
err := pussh.Execute(ctx, pussh.RunnerOptions{
    Image:      "myapp:v1.0.0",
    SSHAddress: "deploy@production.example.com",
})
```

### Cross-Platform Push

```go
import "github.com/gophertribe/pussh"

ctx := context.Background()
err := pussh.Execute(ctx, pussh.RunnerOptions{
    Image:      "myapp:latest",
    SSHAddress: "user@arm-server",
    Platform:   "linux/arm64",
})
```

### Air-Gapped Environment

```go
import "github.com/gophertribe/pussh"

ctx := context.Background()
err := pussh.Execute(ctx, pussh.RunnerOptions{
    Image:             "myapp:latest",
    SSHAddress:        "user@isolated-host",
    ImageTransferMode: "copy", // Transfer unregistry image via SSH
})
```

## Error Handling

The library provides structured error types:

- `SSHError`: SSH connection or command execution failures
- `DockerError`: Docker command failures (local or remote)

Sentinel errors:

- `ErrNoDocker`: Docker not found on remote host
- `ErrDockerPermission`: Cannot run Docker commands on remote host
- `ErrPortExhausted`: No available port found
- `ErrInvalidAddress`: Invalid SSH address format
- `ErrSSHNotFound`: SSH client not found

## Architecture

The library consists of several components:

- **Runner**: Orchestrates the push process
- **SSHConnection**: Manages SSH connections using ControlMaster for efficiency
- **Docker Operations**: Wrappers for local Docker commands
- **Transfer Logic**: Handles image transfer in air-gapped scenarios

## Acknowledgments

This project is heavily inspired by [psviderski/unregistry](https://github.com/psviderski/unregistry) by Pasha Sviderski. The original work is licensed under Apache 2.0. I consider this project a derivative work of Pasha's original project even though it has been rewritten in Go from original Bash implementation.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.

This project is a derivative work of [psviderski/unregistry](https://github.com/psviderski/unregistry) by Pasha Sviderski, which is also licensed under the Apache License, Version 2.0.

