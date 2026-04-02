# Kubernetes Integration

Native client-go integration for multi-cluster Kubernetes operations with SSH-style security tiers.

## Overview

The Kubernetes tool provides direct programmatic access to Kubernetes clusters without requiring `kubectl`. It uses the official `k8s.io/client-go` library for reliable, type-safe operations.

**Key Features:**
- Multi-cluster support via kubeconfig files
- Security tier model (read/modify/dangerous/blocked)
- Namespace restrictions per cluster
- Secret value redaction
- Pod exec, log streaming, port forwarding
- Resource watching with bounded collection

## Quick Start

1. Enable in config:
```json
{
  "kubernetes": {
    "enabled": true,
    "clusters": [
      {
        "name": "prod",
        "kubeconfig_path": "${HOME}/.kube/config",
        "context": "production",
        "safety_level": "read"
      }
    ]
  }
}
```

2. Use via the AI:
```
List pods in the default namespace
Get deployment details for nginx
Show logs from pod web-abc123
```

## Configuration

### Cluster Configuration

```json
{
  "kubernetes": {
    "enabled": true,
    "clusters": [
      {
        "name": "production",
        "kubeconfig_path": "${HOME}/.kube/config",
        "context": "prod-cluster",
        "default_namespace": "app",
        "allowed_namespaces": ["app", "monitoring"],
        "safety_level": "read"
      },
      {
        "name": "staging",
        "kubeconfig_path": "${HOME}/.kube/staging.yaml",
        "safety_level": "modify"
      }
    ],
    "defaults": {
      "namespace": "default",
      "safety_level": "read"
    }
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | bool | Enable the Kubernetes tool |
| `clusters` | array | List of cluster configurations |
| `defaults.namespace` | string | Default namespace when not specified |
| `defaults.safety_level` | string | Default safety level for clusters |

### Cluster Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Unique identifier for the cluster |
| `kubeconfig_path` | string | Path to kubeconfig file (supports `${ENV_VAR}`) |
| `context` | string | Kubeconfig context to use (optional) |
| `default_namespace` | string | Default namespace for this cluster |
| `allowed_namespaces` | array | Restrict access to these namespaces (empty = all) |
| `safety_level` | string | Maximum operation tier allowed |

## Security Model

Operations are classified into tiers, similar to the SSH tool:

| Tier | Operations | Behavior |
|------|------------|----------|
| **read** | get, list, describe, logs, watch, events, top, clusters, namespaces | Auto-approved |
| **modify** | scale, rollout, label, annotate, cordon, uncordon | Confirmation recommended |
| **dangerous** | delete, apply, create, edit, drain, exec, patch | Requires approval |
| **blocked** | Configurable | Always rejected |

### Safety Level Enforcement

Each cluster has a `safety_level` that caps what operations can run:

- `safety_level: "read"` — Only read-tier operations allowed
- `safety_level: "modify"` — Read and modify operations allowed
- `safety_level: "dangerous"` — All operations allowed (use with caution)

### Namespace Restrictions

If `allowed_namespaces` is set, operations are blocked for namespaces not in the list:

```json
{
  "name": "prod",
  "allowed_namespaces": ["app", "monitoring"],
  "safety_level": "read"
}
```

This cluster only allows read operations in `app` and `monitoring` namespaces.

## Tool Actions

### Resource Operations

#### get

Retrieve a single resource or list resources.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `cluster` | Auto | Target cluster (auto-selected if only one) |
| `resource` | Yes | Resource kind (pods, deploy, svc, etc.) |
| `name` | No | Resource name (omit to list all) |
| `namespace` | No | Target namespace |
| `label_selector` | No | Filter by labels (e.g., `app=nginx`) |

```json
{"action": "get", "resource": "pods"}
{"action": "get", "resource": "deploy", "name": "nginx", "namespace": "app"}
{"action": "get", "resource": "pods", "label_selector": "app=web"}
```

**Supported resource kinds:** pods, deployments, services, configmaps, secrets, nodes, namespaces, statefulsets, daemonsets, jobs, cronjobs, ingresses, persistentvolumeclaims, events, replicasets

**Shortnames supported:** po, deploy, svc, cm, sts, ds, cj, ing, pvc, rs, ev, no, ns

#### describe

Get detailed resource description with events.

```json
{"action": "describe", "resource": "pods", "name": "web-abc123"}
{"action": "describe", "resource": "deploy", "name": "nginx", "namespace": "app"}
```

#### logs

Retrieve pod logs.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `name` | Yes | Pod name |
| `namespace` | No | Namespace |
| `container` | No | Container name (defaults to first) |
| `tail_lines` | No | Lines to retrieve (default 100) |
| `since` | No | Seconds ago to start from |

```json
{"action": "logs", "name": "web-abc123"}
{"action": "logs", "name": "web-abc123", "container": "sidecar", "tail_lines": 50}
{"action": "logs", "name": "web-abc123", "since": 3600}
```

Logs are truncated at 32KB.

#### events

List cluster events.

```json
{"action": "events"}
{"action": "events", "namespace": "app"}
{"action": "events", "name": "nginx"}
```

### Modification Operations

#### scale

Scale a deployment or statefulset.

```json
{"action": "scale", "resource": "deploy", "name": "nginx", "replicas": 3}
{"action": "scale", "resource": "sts", "name": "postgres", "namespace": "db", "replicas": 1}
```

#### rollout

Perform rollout operations.

| Subaction | Description |
|-----------|-------------|
| `restart` | Trigger a rolling restart |
| `status` | Show rollout status |
| `history` | Show rollout history |

```json
{"action": "rollout", "resource": "deploy", "name": "nginx", "subaction": "restart"}
{"action": "rollout", "resource": "deploy", "name": "nginx", "subaction": "status"}
```

#### delete

Delete a resource.

```json
{"action": "delete", "resource": "pods", "name": "debug-pod"}
{"action": "delete", "resource": "deploy", "name": "old-app", "namespace": "staging"}
```

### Pod Execution

#### exec

Execute a command inside a pod container.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `name` | Yes | Pod name |
| `command` | Yes | Command to execute |
| `namespace` | No | Namespace |
| `container` | No | Container name (defaults to first) |

```json
{"action": "exec", "name": "web-abc123", "command": "ls -la /app"}
{"action": "exec", "name": "web-abc123", "container": "sidecar", "command": "cat /etc/config"}
```

- Timeout: 30 seconds default
- Output limited to 32KB
- Classified as **dangerous** tier

### Port Forwarding

Create local tunnels to pod ports.

#### portforward_create

```json
{
  "action": "portforward_create",
  "name": "postgres-0",
  "namespace": "db",
  "local_port": 5433,
  "remote_port": 5432
}
```

| Parameter | Required | Description |
|-----------|----------|-------------|
| `name` | Yes | Pod name |
| `local_port` | Yes | Local port (0 for auto-assign, must be >= 1024) |
| `remote_port` | Yes | Remote pod port |
| `namespace` | No | Namespace |

Returns a `forward_id` for managing the tunnel.

#### portforward_list

```json
{"action": "portforward_list"}
```

Returns all active port forwards with their IDs, ports, and creation times.

#### portforward_close

```json
{"action": "portforward_close", "forward_id": "pf-abc123"}
```

- Max 10 concurrent forwards
- Binds to 127.0.0.1 only (security)
- Auto-cleanup on tool shutdown

### Resource Watching

#### watch

Watch for resource changes over a bounded time period.

| Parameter | Required | Description |
|-----------|----------|-------------|
| `resource` | Yes | Resource kind to watch |
| `namespace` | No | Namespace |
| `label_selector` | No | Filter by labels |
| `timeout` | No | Watch duration in seconds (default 30, max 120) |

```json
{"action": "watch", "resource": "pods", "timeout": 60}
{"action": "watch", "resource": "deploy", "namespace": "app", "label_selector": "app=web"}
```

Returns events collected during the watch period:
- `ADDED` — New resource created
- `MODIFIED` — Resource changed
- `DELETED` — Resource removed

Max 100 events per watch. Includes human-readable summaries (e.g., "Pod nginx-abc: Running").

### Cluster Information

#### clusters

List configured clusters and connection status.

```json
{"action": "clusters"}
```

Returns cluster names, default namespaces, connection status, and server versions.

#### namespaces

List namespaces in a cluster.

```json
{"action": "namespaces"}
{"action": "namespaces", "cluster": "production"}
```

## Security Considerations

### Secret Redaction

All secret data values are automatically replaced with `"<REDACTED>"` in tool output. Secret metadata (name, namespace, labels) is visible but actual values are never exposed.

### Cluster Auto-Selection

When only one cluster is configured, it's automatically selected. With multiple clusters, the `cluster` parameter is required.

### Privileged Ports

Port forwarding rejects local ports below 1024 to prevent binding to privileged ports.

### Namespace Isolation

Use `allowed_namespaces` to restrict which namespaces can be accessed per cluster. This is enforced at the tool level before any API call.

## Example Workflows

### Incident Triage

```
1. Get pods in the app namespace with issues
2. Describe the failing pod
3. Get logs from the last 5 minutes
4. Check related events
```

### Deployment Update

```
1. Scale deployment to 0 (with confirmation)
2. Verify pods terminated
3. Scale back to desired replicas
4. Watch for pods to become ready
```

### Database Access

```
1. Create port forward to postgres pod
2. Connect with local psql client
3. Close port forward when done
```

## Troubleshooting

### "unknown cluster" Error

Cluster name doesn't match any configured cluster. Check `kubernetes.clusters[].name` in config.

### "namespace not in allowed list" Error

The namespace is restricted by `allowed_namespaces`. Either add the namespace to the list or remove the restriction.

### "operation exceeds cluster safety level" Error

The operation tier is higher than the cluster's `safety_level`. Increase the safety level or use a different cluster.

### Connection Errors

- Verify kubeconfig path exists
- Check context name is correct
- Ensure cluster is reachable
- Verify credentials are valid

## See Also

- [Configuration Reference](configuration.md) — Full config options
- [Tools Reference](tools-reference.md) — All tools overview
- [SSH Tool](ssh.md) — Similar security model for remote execution
