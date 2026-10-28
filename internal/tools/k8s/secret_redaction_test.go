//go:build with_k8s

package k8s

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Regression tests for H10: only Secret.data was redacted, so values leaked
// through the kubectl last-applied-configuration annotation (and could
// through managedFields and stringData).

const (
	lastAppliedKey = "kubectl.kubernetes.io/last-applied-configuration"
	secretPlain    = "s3cr3t-plain-value"
	secretB64      = "czNjcjN0LWI2NC12YWx1ZQ==" // "s3cr3t-b64-value"
)

func appliedSecret() *corev1.Secret {
	last := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"db","namespace":"default"},` +
		`"data":{"password":"` + secretB64 + `"},"stringData":{"token":"` + secretPlain + `"}}`
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "db", Namespace: "default",
			Labels:      map[string]string{"app": "db"},
			Annotations: map[string]string{lastAppliedKey: last, "owner": "team-alpha"},
			ManagedFields: []metav1.ManagedFieldsEntry{{
				Manager: "kubectl-client-side-apply", Operation: metav1.ManagedFieldsOperationUpdate,
				FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:data":{"f:password":{}}}`)},
			}},
		},
		Data:       map[string][]byte{"password": []byte("s3cr3t-b64-value")},
		StringData: map[string]string{"token": secretPlain},
	}
}

func assertNoSecretValues(t *testing.T, v interface{}) {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	for _, leak := range []string{secretB64, secretPlain, "s3cr3t-b64-value", "czNjcjN0"} {
		assert.NotContains(t, string(b), leak)
	}
}

func assertRedactedSecretMap(t *testing.T, m map[string]interface{}) {
	t.Helper()
	assertNoSecretValues(t, m)
	meta, _ := m["metadata"].(map[string]interface{})
	require.NotNil(t, meta)
	assert.NotContains(t, meta, "managedFields")
	ann, _ := meta["annotations"].(map[string]interface{})
	assert.Equal(t, "<REDACTED>", ann[lastAppliedKey])
	assert.Equal(t, "team-alpha", ann["owner"], "other annotations are kept")
	assert.Equal(t, map[string]interface{}{"password": "<REDACTED>"}, m["data"])
	if sd, ok := m["stringData"]; ok {
		assert.Equal(t, map[string]interface{}{"token": "<REDACTED>"}, sd)
	}
}

func TestSecretRedaction_LastAppliedManagedFieldsStringData(t *testing.T) {
	ctx := context.Background()
	cc := &ClusterClient{name: "c", clientset: fake.NewSimpleClientset(appliedSecret()), namespace: "default"}

	m, err := cc.GetResource(ctx, "secrets", "db", "default")
	require.NoError(t, err)
	assertRedactedSecretMap(t, m)

	items, err := cc.ListResources(ctx, "secret", "default", "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assertRedactedSecretMap(t, items[0])

	desc, err := cc.DescribeResource(ctx, "secrets", "db", "default")
	require.NoError(t, err)
	assertNoSecretValues(t, desc)
}

func TestSecretRedaction_Function(t *testing.T) {
	m, err := toMap(appliedSecret())
	require.NoError(t, err)
	redactSecretData(m)
	assertRedactedSecretMap(t, m)

	// Objects without metadata or data do not panic.
	redactSecretData(map[string]interface{}{})
	redactSecretData(map[string]interface{}{"metadata": "odd"})
}

func TestSecretRedaction_NonSecretsUntouched(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default",
			Annotations: map[string]string{lastAppliedKey: `{"data":{"k":"v"}}`}},
		Data: map[string]string{"k": "v"},
	}
	cc := &ClusterClient{name: "c", clientset: fake.NewSimpleClientset(cm), namespace: "default"}
	m, err := cc.GetResource(context.Background(), "configmaps", "cfg", "default")
	require.NoError(t, err)
	meta := m["metadata"].(map[string]interface{})
	assert.Equal(t, `{"data":{"k":"v"}}`, meta["annotations"].(map[string]interface{})[lastAppliedKey])
}

func TestK8sTool_SecretValuesNeverReturned(t *testing.T) {
	tool := setupTestTool(t)
	cc, err := tool.clients.GetClient("test-cluster")
	require.NoError(t, err)
	_, err = cc.clientset.CoreV1().Secrets("default").Create(context.Background(), appliedSecret(), metav1.CreateOptions{})
	require.NoError(t, err)

	for _, args := range []map[string]interface{}{
		{"action": "get", "resource": "secrets", "name": "db"},
		{"action": "get", "resource": "secrets"},
		{"action": "describe", "resource": "secret", "name": "db"},
	} {
		res, err := tool.Execute(context.Background(), args)
		require.NoError(t, err)
		require.True(t, res.Success, "%v: %s", args, res.Error)
		assertNoSecretValues(t, res)
	}
}
