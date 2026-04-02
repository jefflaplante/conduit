# Kubernetes Tool Design Spec

## Overview

Native client-go integration for multi-cluster Kubernetes operations with SSH-style security tiers. Enables AI-driven k8s operations: read pods/deployments/services/logs, exec into pods, scale/rollout, port-forward, watch events. Follows the SSH tool pattern exactly.

## Package Layout

`internal/tools/k8s/` with these files:

| File | Purpose |
|------|---------|
| `security.go` | Operation tier classification (read/modify/dangerous/blocked) + namespace enforcement |
| `client.go` | client-go clientset init, multi-cluster management, resource CRUD operations |
| `tool.go` | Tool interface (Name/Description/Parameters/Execute), action dispatch, registry wiring |
| `exec.go` | Pod exec via `remotecommand` + log streaming (follow/tail) |
| `portforward.go` | Port forwarding via client-go `portforward` package |
| `watch.go` | Resource watch with event streaming |

Each file has a corresponding `*_test.go`.

## Configuration

Existing `KubernetesConfig` in `internal/config/kubernetes.go` (already implemented, ticket conduit-25i closed):

```go
type KubernetesConfig struct {
    Enabled  bool
    Clusters []KubernetesCluster
    Defaults KubernetesDefaults
}

type KubernetesCluster struct {
    Name              string
    KubeconfigPath    string
    Context           string
    DefaultNamespace  string
    AllowedNamespaces []string
    SafetyLevel       string // "read", "modify", "dangerous"
}
```

No config changes needed.

## Security Engine (`security.go`)

Reuses the SSH tool's tier model adapted to K8s operations.

### Security Tiers

| Tier | Operations |
|------|-----------|
| Read | get, list, describe, logs, top, events, namespaces, clusters |
| Modify | scale, rollout restart, label, annotate, cordon, uncordon |
| Dangerous | delete, apply, create, edit, drain, exec, patch |
| Blocked | Configurable (e.g., delete namespace, delete node) |

### Structs

```go
type SecurityTier string

const (
    TierRead      SecurityTier = "read"
    TierModify    SecurityTier = "modify"
    TierDangerous SecurityTier = "dangerous"
    TierBlocked   SecurityTier = "blocked"
)

type OperationClassification struct {
    Tier             SecurityTier
    Action           string
    Resource         string
    Namespace        string
    Reason           string
    RequiresApproval bool
    Blocked          bool
    Warnings         []string
}

type SecurityEngine struct {
    config          KubernetesSecurityConfig
    readOps         map[string]bool
    modifyOps       map[string]bool
    dangerousOps    map[string]bool
    blockedOps      map[string]bool
    blockedResources map[string]map[string]bool // action -> resource -> blocked
}
```

### Key Methods

- `NewSecurityEngine(cfg KubernetesSecurityConfig) *SecurityEngine`
- `ClassifyOperation(action, resource, namespace string) *OperationClassification`
- `ValidateNamespace(cluster KubernetesCluster, namespace string) error` — enforces AllowedNamespaces
- `ValidateForCluster(classification *OperationClassification, cluster KubernetesCluster) error` — checks cluster SafetyLevel

### Security Config

Add to the existing KubernetesConfig (extend, don't modify existing fields):

```go
type KubernetesSecurityConfig struct {
    RequireApproval  []string            // tiers requiring approval, e.g. ["dangerous"]
    BlockedActions   []BlockedAction     // specific action+resource combos to block
    ApprovalTimeout  time.Duration
    ApprovalChannel  string
}

type BlockedAction struct {
    Action   string // e.g., "delete"
    Resource string // e.g., "namespace", "node", "*"
}
```

## Core Client (`client.go`)

### Structs

```go
type ClusterClient struct {
    name      string
    clientset kubernetes.Interface
    config    *rest.Config
    namespace string // effective default namespace
}

type ClientManager struct {
    clusters map[string]*ClusterClient
    mu       sync.RWMutex
}
```

### Key Methods

- `NewClientManager(cfg *config.KubernetesConfig) (*ClientManager, error)` — lazy init, validates kubeconfig paths exist
- `GetClient(clusterName string) (*ClusterClient, error)` — returns or creates clientset
- `ListClusters() []ClusterInfo` — returns configured cluster names and status
- `Close()` — cleanup

### Resource Operations on ClusterClient

- `GetResource(ctx, kind, name, namespace string) (map[string]interface{}, error)`
- `ListResources(ctx, kind, namespace string, labelSelector string) ([]map[string]interface{}, error)`
- `DescribeResource(ctx, kind, name, namespace string) (string, error)` — detailed human-readable output
- `DeleteResource(ctx, kind, name, namespace string) error`
- `ScaleResource(ctx, kind, name, namespace string, replicas int32) error`
- `RolloutRestart(ctx, kind, name, namespace string) error`
- `GetLogs(ctx, pod, namespace, container string, tailLines int64, follow bool) (io.ReadCloser, error)`
- `GetEvents(ctx, namespace string, fieldSelector string) ([]map[string]interface{}, error)`
- `ListNamespaces(ctx) ([]string, error)`
- `TopPods(ctx, namespace string) ([]map[string]interface{}, error)` — requires metrics API

Supported resource kinds: pods, deployments, services, configmaps, secrets (redacted), nodes, events, statefulsets, daemonsets, jobs, cronjobs, ingresses, persistentvolumeclaims.

Secret values replaced with `"<REDACTED>"` in all output.

## Tool Interface (`tool.go`)

### Actions

| Action | Security Tier | Parameters |
|--------|--------------|------------|
| `get` | read | cluster, resource, name, namespace, label_selector |
| `describe` | read | cluster, resource, name, namespace |
| `logs` | read | cluster, pod, namespace, container, tail_lines, since |
| `exec` | dangerous | cluster, pod, namespace, container, command |
| `scale` | modify | cluster, resource, name, namespace, replicas |
| `rollout` | modify | cluster, resource, name, namespace, subaction (restart/status/history) |
| `delete` | dangerous | cluster, resource, name, namespace |
| `apply` | dangerous | cluster, manifest (JSON/YAML string) |
| `top` | read | cluster, namespace, resource (pods/nodes) |
| `clusters` | read | (none) |
| `namespaces` | read | cluster |
| `events` | read | cluster, namespace, field_selector |
| `portforward_create` | read | cluster, pod, namespace, local_port, remote_port |
| `portforward_close` | read | id |
| `portforward_list` | read | (none) |
| `watch` | read | cluster, resource, namespace, label_selector, timeout |

### Struct

```go
type K8sTool struct {
    services       *types.ToolServices
    config         *config.KubernetesConfig
    security       *SecurityEngine
    clients        *ClientManager
    portForwarder  *PortForwarder
}
```

### Constructor

```go
func NewK8sTool(services *types.ToolServices, cfg *config.KubernetesConfig) (*K8sTool, error)
```

Creates SecurityEngine, ClientManager, PortForwarder. Returns ready-to-use tool.

### Registry Integration

In `registry.go`, conditional registration (same pattern as SSH):

```go
if r.services.ConfigMgr != nil && r.services.ConfigMgr.Kubernetes.Enabled {
    k8sTool, err := k8s.NewK8sTool(r.services, &r.services.ConfigMgr.Kubernetes)
    if err != nil {
        log.Printf("Failed to create K8s tool: %v", err)
    } else {
        allTools = append(allTools, k8sTool)
    }
}
```

## Pod Exec & Log Streaming (`exec.go`)

- Pod exec via `k8s.io/client-go/tools/remotecommand` — SPDY executor
- Command execution with configurable timeout (default 30s)
- Container selection for multi-container pods (defaults to first container)
- Log streaming via corev1 `GetLogs` with PodLogOptions (TailLines, Follow, SinceSeconds, Container)
- Follow mode bounded by timeout to prevent infinite streaming
- Output truncated to `MaxToolResultChars` from config

## Port Forwarding (`portforward.go`)

- Local port forward to pod ports via `k8s.io/client-go/tools/portforward`
- Lifecycle management (create/close/list)
- Bound to 127.0.0.1 only
- Auto-assign local port if 0
- Max concurrent forwards (default 10)
- Cleanup on tool shutdown

### Struct

```go
type PortForwarder struct {
    forwards map[string]*activeForward
    mu       sync.RWMutex
    maxForwards int
}

type activeForward struct {
    ID         string
    Cluster    string
    Pod        string
    Namespace  string
    LocalPort  int
    RemotePort int
    stopChan   chan struct{}
    readyChan  chan struct{}
    CreatedAt  time.Time
}
```

## Watch & Events (`watch.go`)

- Resource watch via client-go `watch.Interface`
- Returns events within a timeout window (default 30s, max 120s)
- Collects Added/Modified/Deleted events
- Returns structured event list with timestamps
- No persistent background watches — each invocation is bounded

## Testing Strategy

- Unit tests with fake clientsets (`k8s.io/client-go/kubernetes/fake`)
- Security engine fully testable without K8s cluster (pure logic)
- Port forwarder tested with validation logic (port ranges, limits)
- Integration tests (tagged `_integration_test.go`) for real cluster testing

## Implementation Waves

1. **Wave 1** (parallel): Security Engine (`security.go`) + Core Client (`client.go`) — independent
2. **Wave 2**: Tool Interface (`tool.go`) + registry wiring — depends on Wave 1
3. **Wave 3** (parallel): Pod Exec/Logs (`exec.go`) + Port Forward (`portforward.go`) + Watch (`watch.go`) — independent extensions of Wave 2

## Dependencies to Add

```
k8s.io/client-go
k8s.io/api
k8s.io/apimachinery
k8s.io/metrics (for top command)
```
