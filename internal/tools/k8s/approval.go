//go:build with_k8s

package k8s

import (
	"context"
	"fmt"
	"strings"

	"conduit/internal/approval"
	"conduit/internal/tools/approvalgate"
	"conduit/internal/tools/types"
)

// DefaultRequireApproval is the tier list used when kubernetes.require_approval
// is absent from the config (conduit-c8ct): dangerous operations (delete,
// exec, ...) need a human approval. An explicit empty list disables it.
var DefaultRequireApproval = []string{string(TierDangerous)}

// opField is one operation-specific parameter: key is bound into the
// approval fingerprint, label/value are shown to the human.
type opField struct {
	key   string
	label string
	value string
}

// k8sOp is the frozen description of one Kubernetes operation.
type k8sOp struct {
	Cluster   string
	Namespace string
	Verb      string
	Resource  string
	Name      string
	Extra     []opField
}

// target renders "resource/name" (or just resource for list/watch).
func (op k8sOp) target() string {
	if op.Name == "" {
		return op.Resource
	}
	return op.Resource + "/" + op.Name
}

// operation builds the approval request for op. Displayed and audited
// values are redacted; Params keep the exact values the approval binds.
func (op k8sOp) operation(cls *OperationClassification) approvalgate.Operation {
	target := op.target()
	params := map[string]string{
		"cluster":   op.Cluster,
		"namespace": op.Namespace,
		"verb":      op.Verb,
		"resource":  op.Resource,
		"name":      op.Name,
	}
	audit := map[string]string{
		"cluster":   op.Cluster,
		"namespace": op.Namespace,
		"verb":      op.Verb,
		"resource":  op.Resource,
		"name":      op.Name,
		"tier":      string(cls.Tier),
	}
	fields := []approval.Field{
		{Name: "Cluster", Value: op.Cluster},
		{Name: "Namespace", Value: op.Namespace},
		{Name: "Verb", Value: op.Verb},
		{Name: "Resource", Value: target},
	}
	for _, f := range op.Extra {
		params["arg."+f.key] = f.value
		if f.value == "" {
			continue
		}
		shown := approvalgate.Clip(approvalgate.RedactCommand(f.value), approvalgate.MaxFieldRunes)
		fields = append(fields, approval.Field{Name: f.label, Value: shown})
		audit["arg."+f.key] = approvalgate.Clip(shown, 200)
	}
	fields = append(fields, approval.Field{Name: "Risk", Value: riskText(cls)})

	return approvalgate.Operation{
		Kind: "k8s." + strings.ReplaceAll(op.Verb, " ", "_"),
		Title: fmt.Sprintf("Kubernetes %s %s in namespace %s on cluster %s",
			strings.ToUpper(op.Verb), target, op.Namespace, op.Cluster),
		Fields:  fields,
		Params:  params,
		Audit:   audit,
		Summary: fmt.Sprintf("Kubernetes %s of %s in namespace %s on cluster %s", op.Verb, target, op.Namespace, op.Cluster),
	}
}

func riskText(cls *OperationClassification) string {
	s := fmt.Sprintf("%s tier (%s)", cls.Tier, cls.Reason)
	if len(cls.Warnings) > 0 {
		s += "; " + strings.Join(cls.Warnings, "; ")
	}
	return s
}

// authorize runs op directly unless its tier requires human approval, in
// which case the operation is frozen and handed to the approval manager:
// run executes later, exactly once, only if the human approves
// (conduit-c8ct). Non-interactive turns and a missing approver fail closed.
func (t *K8sTool) authorize(ctx context.Context, cls *OperationClassification, op k8sOp, run approvalgate.Run) (*types.ToolResult, error) {
	if cls == nil || !cls.RequiresApproval {
		return run(ctx)
	}
	return approvalgate.Request(ctx, t.approver(), op.operation(cls), run), nil
}

func (t *K8sTool) approver() approval.Requester {
	if t.services == nil {
		return nil
	}
	return t.services.Approvals
}
