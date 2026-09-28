//go:build with_ssh

package ssh

import (
	"context"
	"fmt"
	"os"
	"time"

	"conduit/internal/sandbox"
	toolargs "conduit/internal/tools/args"
	"conduit/internal/tools/types"
)

// Fixed tiers for operations that are not shell commands (conduit-enf0).
// An upload writes arbitrary bytes to an arbitrary remote path
// (authorized_keys, cron.d, systemd units), which is as powerful as running
// a command, so it is dangerous-tier: approval-gated under the default
// require_approval and refused on hosts capped at read or modify. A download
// only reads remotely (its local write is sandbox-confined), so it is
// read-tier; it is still gated if an owner lists "read" in require_approval.
const (
	scpUploadTier   = TierDangerous
	scpDownloadTier = TierRead
)

// operationBlocked is the refusal for a blocked non-command operation.
func operationBlocked(c *ClassificationResult, data map[string]interface{}) *types.ToolResult {
	data["tier"] = string(c.Tier)
	data["reason"] = c.Reason
	data["operation"] = c.BaseCommand
	return &types.ToolResult{
		Success: false,
		Error:   fmt.Sprintf("%s blocked: %s", c.BaseCommand, c.Reason),
		Data:    data,
	}
}

// SetSandbox sets the filesystem sandbox that SCP local paths must resolve
// inside. conduit-31jg.69
func (t *SSHTool) SetSandbox(sb *sandbox.Sandbox) {
	t.sandbox = sb
}

// resolveSCPLocalPath canonicalizes an SCP local path and checks it against
// the sandbox (symlinks resolved), so the path that was checked is the path
// that is read or written. Previously scp_upload read any local file the
// gateway user could (e.g. ~/.ssh/id_ed25519) and ship it to a remote host.
// conduit-31jg.69
func (t *SSHTool) resolveSCPLocalPath(localPath string) (string, *types.ToolResult) {
	resolved, err := t.sandbox.Resolve(localPath)
	if err != nil {
		return "", types.NewErrorResult("path_not_allowed",
			fmt.Sprintf("local_path %q is not allowed: %v", localPath, err)).
			WithParameter("local_path", localPath).
			WithSuggestions([]string{"Use a path inside tools.sandbox.workspace_dir or tools.sandbox.allowed_paths"})
	}
	return resolved, nil
}

// scpUpload uploads a local file to a remote host via SCP
func (t *SSHTool) scpUpload(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := toolargs.GetString(args, "host", "")
	localPath := toolargs.GetString(args, "local_path", "")
	remotePath := toolargs.GetString(args, "remote_path", "")

	// Validate required parameters
	if host == "" {
		return types.NewErrorResult("missing_parameter", "host parameter is required for scp_upload action").
			WithParameter("host", nil).
			WithSuggestions([]string{"Use action=hosts to list available hosts"}), nil
	}

	if localPath == "" {
		return types.NewErrorResult("missing_parameter", "local_path parameter is required for scp_upload action").
			WithParameter("local_path", nil), nil
	}

	if remotePath == "" {
		return types.NewErrorResult("missing_parameter", "remote_path parameter is required for scp_upload action").
			WithParameter("remote_path", nil), nil
	}

	// Look up host configuration
	hostConfig := t.config.GetHostByName(host)
	if hostConfig == nil {
		availableHosts := t.getHostNames()
		return types.NewErrorResult("invalid_host", fmt.Sprintf("host '%s' not found in configuration", host)).
			WithParameter("host", host).
			WithAvailableValues(availableHosts).
			WithSuggestions([]string{"Use action=hosts to see all configured hosts"}), nil
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("host '%s' is disabled", host),
		}, nil
	}

	// conduit-31jg.69: confine the upload source to the sandbox.
	resolvedLocal, denied := t.resolveSCPLocalPath(localPath)
	if denied != nil {
		return denied, nil
	}

	// Check if local file exists and get its info
	localInfo, err := os.Stat(resolvedLocal)
	if err != nil {
		if os.IsNotExist(err) {
			return &types.ToolResult{
				Success: false,
				Error:   fmt.Sprintf("local file not found: %s", localPath),
			}, nil
		}
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to stat local file: %v", err),
		}, nil
	}

	if localInfo.IsDir() {
		return &types.ToolResult{
			Success: false,
			Error:   "local_path is a directory; only single files are supported in v1",
		}, nil
	}

	// conduit-enf0: classify the actual operation against the host tier.
	classification := t.securityEngine.ClassifyOperationForHost("scp_upload", scpUploadTier,
		"uploads write arbitrary content to a remote path", hostConfig.SecurityTier, remotePath)
	if classification.Blocked {
		return operationBlocked(classification, map[string]interface{}{
			"host": host, "local_path": localPath, "remote_path": remotePath,
		}), nil
	}

	// conduit-w3l7: gated uploads are frozen (host, resolved local path,
	// remote path) and run only after a human "YES <code>" reply.
	if classification.RequiresApproval {
		size := localInfo.Size()
		return t.gate(ctx, t.uploadOperation(host, localPath, resolvedLocal, remotePath, size, classification),
			func(context.Context) (*types.ToolResult, error) {
				info, err := os.Stat(resolvedLocal)
				if err != nil || info.IsDir() {
					return &types.ToolResult{Success: false, Error: fmt.Sprintf("local file %s is no longer a readable file; nothing was uploaded", localPath)}, nil
				}
				if info.Size() != size {
					return &types.ToolResult{Success: false, Error: fmt.Sprintf("local file %s changed size since approval (%d -> %d bytes); nothing was uploaded", localPath, size, info.Size())}, nil
				}
				return t.runUpload(host, localPath, resolvedLocal, remotePath, info)
			})
	}

	return t.runUpload(host, localPath, resolvedLocal, remotePath, localInfo)
}

// runUpload performs an authorized SCP upload.
func (t *SSHTool) runUpload(host, localPath, resolvedLocal, remotePath string, localInfo os.FileInfo) (*types.ToolResult, error) {
	// Get SSH client for the host
	sshClient, release, err := t.acquireSSHClient(host)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to connect to host: %v", err),
		}, nil
	}
	defer release()

	// Create SCP client
	scpClient := NewSCPClient(sshClient)

	// Perform the upload
	startTime := time.Now()
	if err := scpClient.Upload(resolvedLocal, remotePath, 0); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("SCP upload failed: %v", err),
			Data: map[string]interface{}{
				"local_path":  localPath,
				"remote_path": remotePath,
				"host":        host,
			},
		}, nil
	}
	duration := time.Since(startTime)

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("File uploaded successfully:\n  Local: %s\n  Remote: %s\n  Size: %d bytes\n  Duration: %v",
			localPath, remotePath, localInfo.Size(), duration),
		Data: map[string]interface{}{
			"local_path":  localPath,
			"remote_path": remotePath,
			"host":        host,
			"size":        localInfo.Size(),
			"duration_ms": duration.Milliseconds(),
		},
	}, nil
}

// scpDownload downloads a file from a remote host to a local path via SCP
func (t *SSHTool) scpDownload(ctx context.Context, args map[string]interface{}) (*types.ToolResult, error) {
	host := toolargs.GetString(args, "host", "")
	remotePath := toolargs.GetString(args, "remote_path", "")
	localPath := toolargs.GetString(args, "local_path", "")

	// Validate required parameters
	if host == "" {
		return types.NewErrorResult("missing_parameter", "host parameter is required for scp_download action").
			WithParameter("host", nil).
			WithSuggestions([]string{"Use action=hosts to list available hosts"}), nil
	}

	if remotePath == "" {
		return types.NewErrorResult("missing_parameter", "remote_path parameter is required for scp_download action").
			WithParameter("remote_path", nil), nil
	}

	if localPath == "" {
		return types.NewErrorResult("missing_parameter", "local_path parameter is required for scp_download action").
			WithParameter("local_path", nil), nil
	}

	// Look up host configuration
	hostConfig := t.config.GetHostByName(host)
	if hostConfig == nil {
		availableHosts := t.getHostNames()
		return types.NewErrorResult("invalid_host", fmt.Sprintf("host '%s' not found in configuration", host)).
			WithParameter("host", host).
			WithAvailableValues(availableHosts).
			WithSuggestions([]string{"Use action=hosts to see all configured hosts"}), nil
	}

	// Check if host is enabled
	if !hostConfig.IsHostEnabled() {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("host '%s' is disabled", host),
		}, nil
	}

	// conduit-31jg.69: confine the download destination to the sandbox.
	resolvedLocal, denied := t.resolveSCPLocalPath(localPath)
	if denied != nil {
		return denied, nil
	}

	// conduit-enf0: classify against the host tier; gate if "read" is in
	// require_approval (downloads were previously never gated).
	classification := t.securityEngine.ClassifyOperationForHost("scp_download", scpDownloadTier,
		"downloads read a remote file", hostConfig.SecurityTier, remotePath)
	if classification.Blocked {
		return operationBlocked(classification, map[string]interface{}{
			"host": host, "local_path": localPath, "remote_path": remotePath,
		}), nil
	}
	if classification.RequiresApproval {
		return t.gate(ctx, t.downloadOperation(host, remotePath, localPath, resolvedLocal, classification),
			func(context.Context) (*types.ToolResult, error) {
				// The destination must still resolve to the approved path
				// (e.g. no symlink swapped in while awaiting approval).
				again, denied := t.resolveSCPLocalPath(localPath)
				if denied != nil || again != resolvedLocal {
					return &types.ToolResult{Success: false, Error: fmt.Sprintf("local path %s no longer resolves to the approved destination; nothing was downloaded", localPath)}, nil
				}
				return t.runDownload(host, remotePath, localPath, resolvedLocal)
			})
	}

	return t.runDownload(host, remotePath, localPath, resolvedLocal)
}

// runDownload performs an authorized SCP download into resolvedLocal.
func (t *SSHTool) runDownload(host, remotePath, localPath, resolvedLocal string) (*types.ToolResult, error) {
	// Get SSH client for the host
	sshClient, release, err := t.acquireSSHClient(host)
	if err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to connect to host: %v", err),
		}, nil
	}
	defer release()

	// Create SCP client
	scpClient := NewSCPClient(sshClient)

	// Perform the download
	startTime := time.Now()
	if err := scpClient.Download(remotePath, resolvedLocal); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("SCP download failed: %v", err),
			Data: map[string]interface{}{
				"remote_path": remotePath,
				"local_path":  localPath,
				"host":        host,
			},
		}, nil
	}
	duration := time.Since(startTime)

	// Get file info after download
	localInfo, err := os.Stat(resolvedLocal)
	var fileSize int64
	if err == nil {
		fileSize = localInfo.Size()
	}

	return &types.ToolResult{
		Success: true,
		Content: fmt.Sprintf("File downloaded successfully:\n  Remote: %s\n  Local: %s\n  Size: %d bytes\n  Duration: %v",
			remotePath, localPath, fileSize, duration),
		Data: map[string]interface{}{
			"remote_path": remotePath,
			"local_path":  localPath,
			"host":        host,
			"size":        fileSize,
			"duration_ms": duration.Milliseconds(),
		},
	}, nil
}
