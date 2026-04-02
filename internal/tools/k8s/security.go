package k8s

import (
	"fmt"
	"strings"
)

// SecurityTier represents the risk level of a Kubernetes operation.
type SecurityTier string

const (
	TierRead      SecurityTier = "read"
	TierModify    SecurityTier = "modify"
	TierDangerous SecurityTier = "dangerous"
	TierBlocked   SecurityTier = "blocked"
)

// OperationClassification holds the security assessment for a single K8s operation.
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

// SecurityConfig drives the security engine's behaviour via JSON config.
type SecurityConfig struct {
	RequireApproval []string        `json:"require_approval,omitempty"`
	BlockedActions  []BlockedAction `json:"blocked_actions,omitempty"`
	ApprovalTimeout int             `json:"approval_timeout,omitempty"`
	ApprovalChannel string          `json:"approval_channel,omitempty"`
}

// BlockedAction pairs an action with a resource to unconditionally block.
type BlockedAction struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// SecurityEngine classifies Kubernetes operations into security tiers.
type SecurityEngine struct {
	config           SecurityConfig
	readOps          map[string]bool
	modifyOps        map[string]bool
	dangerousOps     map[string]bool
	blockedResources map[string]map[string]bool // action -> resource -> blocked
}

// NewSecurityEngine creates a SecurityEngine with default tier mappings and
// blocked-resource rules derived from cfg.
func NewSecurityEngine(cfg SecurityConfig) *SecurityEngine {
	se := &SecurityEngine{
		config: cfg,
		readOps: map[string]bool{
			"get":        true,
			"list":       true,
			"describe":   true,
			"logs":       true,
			"top":        true,
			"events":     true,
			"clusters":   true,
			"namespaces": true,
		},
		modifyOps: map[string]bool{
			"scale":    true,
			"rollout":  true,
			"label":    true,
			"annotate": true,
			"cordon":   true,
			"uncordon": true,
		},
		dangerousOps: map[string]bool{
			"delete": true,
			"apply":  true,
			"create": true,
			"edit":   true,
			"drain":  true,
			"exec":   true,
			"patch":  true,
		},
		blockedResources: make(map[string]map[string]bool),
	}

	for _, ba := range cfg.BlockedActions {
		action := strings.ToLower(ba.Action)
		resource := strings.ToLower(ba.Resource)
		if se.blockedResources[action] == nil {
			se.blockedResources[action] = make(map[string]bool)
		}
		se.blockedResources[action][resource] = true
	}

	return se
}

// ClassifyOperation determines the security tier for a given action+resource pair.
func (se *SecurityEngine) ClassifyOperation(action, resource, namespace string) *OperationClassification {
	action = strings.ToLower(strings.TrimSpace(action))
	resource = strings.ToLower(strings.TrimSpace(resource))
	namespace = strings.TrimSpace(namespace)

	c := &OperationClassification{
		Action:    action,
		Resource:  resource,
		Namespace: namespace,
	}

	// Check blocked resources first (specific resource then wildcard).
	if resMap, ok := se.blockedResources[action]; ok {
		if resMap[resource] || resMap["*"] {
			c.Tier = TierBlocked
			c.Blocked = true
			c.Reason = fmt.Sprintf("action %q on resource %q is blocked by policy", action, resource)
			return c
		}
	}

	// Classify into tier.
	switch {
	case se.readOps[action]:
		c.Tier = TierRead
		c.Reason = fmt.Sprintf("%q is a read-only operation", action)
	case se.modifyOps[action]:
		c.Tier = TierModify
		c.Reason = fmt.Sprintf("%q is a modify operation", action)
	case se.dangerousOps[action]:
		c.Tier = TierDangerous
		c.Reason = fmt.Sprintf("%q is a dangerous operation", action)
		c.Warnings = append(c.Warnings, fmt.Sprintf("action %q can cause data loss or service disruption", action))
	default:
		c.Tier = TierDangerous
		c.Reason = fmt.Sprintf("unknown action %q defaults to dangerous tier", action)
		c.Warnings = append(c.Warnings, "unknown action classified as dangerous")
	}

	c.RequiresApproval = se.requiresApproval(c.Tier)
	return c
}

// ValidateNamespace checks that namespace is in the allowed list. An empty
// allowedNamespaces slice means all namespaces are permitted.
func (se *SecurityEngine) ValidateNamespace(allowedNamespaces []string, namespace string) error {
	if len(allowedNamespaces) == 0 {
		return nil
	}
	for _, allowed := range allowedNamespaces {
		if strings.EqualFold(allowed, namespace) {
			return nil
		}
	}
	return fmt.Errorf("namespace %q is not in the allowed list %v", namespace, allowedNamespaces)
}

// ValidateForCluster ensures the operation's tier does not exceed the cluster's
// safety level. For example a cluster with safety level "read" only permits
// read operations.
func (se *SecurityEngine) ValidateForCluster(classification *OperationClassification, clusterSafetyLevel string) error {
	maxSeverity := tierSeverity(SecurityTier(strings.ToLower(clusterSafetyLevel)))
	opSeverity := tierSeverity(classification.Tier)

	if opSeverity > maxSeverity {
		return fmt.Errorf(
			"operation %q (tier %s, severity %d) exceeds cluster safety level %q (max severity %d)",
			classification.Action, classification.Tier, opSeverity,
			clusterSafetyLevel, maxSeverity,
		)
	}
	return nil
}

// tierSeverity returns a numeric severity for ordering tiers.
func tierSeverity(tier SecurityTier) int {
	switch tier {
	case TierRead:
		return 1
	case TierModify:
		return 2
	case TierDangerous:
		return 3
	case TierBlocked:
		return 4
	default:
		return 3 // unknown defaults to dangerous severity
	}
}

// requiresApproval checks whether the given tier is listed in RequireApproval.
func (se *SecurityEngine) requiresApproval(tier SecurityTier) bool {
	t := string(tier)
	for _, ra := range se.config.RequireApproval {
		if strings.EqualFold(ra, t) {
			return true
		}
	}
	return false
}
