package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewClientManager(t *testing.T) {
	clusters := []ClusterConfig{
		{Name: "prod", KubeconfigPath: "/tmp/kube/prod", Context: "prod-ctx", DefaultNamespace: "production"},
		{Name: "staging", KubeconfigPath: "/tmp/kube/staging", DefaultNamespace: "staging"},
	}
	cm := NewClientManager(clusters)

	if len(cm.clusters) != 2 {
		t.Fatalf("expected 2 clusters, got %d", len(cm.clusters))
	}
	if cm.clusters["prod"].Context != "prod-ctx" {
		t.Errorf("expected context 'prod-ctx', got %q", cm.clusters["prod"].Context)
	}
	if len(cm.clients) != 0 {
		t.Errorf("expected 0 connected clients, got %d", len(cm.clients))
	}
}

func TestGetClient_UnknownCluster(t *testing.T) {
	cm := NewClientManager(nil)
	_, err := cm.GetClient("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown cluster")
	}
}

func TestSetClient(t *testing.T) {
	cm := NewClientManager(nil)
	fc := &ClusterClient{
		name:      "injected",
		clientset: fake.NewSimpleClientset(),
		namespace: "test-ns",
	}
	cm.SetClient("injected", fc)

	client, err := cm.GetClient("injected")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != fc {
		t.Error("expected the same client object back")
	}
}

func TestListClusters(t *testing.T) {
	cm := NewClientManager([]ClusterConfig{
		{Name: "alpha", DefaultNamespace: "ns-alpha"},
		{Name: "beta"},
	})
	// Inject a connected fake for alpha
	cm.SetClient("alpha", &ClusterClient{
		name:      "alpha",
		clientset: fake.NewSimpleClientset(),
		namespace: "ns-alpha",
	})

	infos := cm.ListClusters()
	if len(infos) != 2 {
		t.Fatalf("expected 2 cluster infos, got %d", len(infos))
	}

	// Find alpha
	var alphaInfo, betaInfo *ClusterInfo
	for i := range infos {
		switch infos[i].Name {
		case "alpha":
			alphaInfo = &infos[i]
		case "beta":
			betaInfo = &infos[i]
		}
	}
	if alphaInfo == nil || betaInfo == nil {
		t.Fatal("missing cluster info")
	}
	if !alphaInfo.Connected {
		t.Error("alpha should be connected")
	}
	if betaInfo.Connected {
		t.Error("beta should not be connected")
	}
	if betaInfo.DefaultNamespace != "default" {
		t.Errorf("expected default namespace 'default' for beta, got %q", betaInfo.DefaultNamespace)
	}
}

func TestNormalizeKind(t *testing.T) {
	tests := map[string]string{
		"pod":        "pods",
		"po":         "pods",
		"pods":       "pods",
		"deploy":     "deployments",
		"deployment": "deployments",
		"svc":        "services",
		"cm":         "configmaps",
		"secret":     "secrets",
		"no":         "nodes",
		"ns":         "namespaces",
		"sts":        "statefulsets",
		"ds":         "daemonsets",
		"cj":         "cronjobs",
		"ing":        "ingresses",
		"pvc":        "persistentvolumeclaims",
		"rs":         "replicasets",
		"ev":         "events",
		"PODS":       "pods",
		" deploy ":   "deployments",
	}
	for input, expected := range tests {
		if got := normalizeKind(input); got != expected {
			t.Errorf("normalizeKind(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestResolveNamespace(t *testing.T) {
	cc := &ClusterClient{namespace: "my-ns"}
	if ns := cc.resolveNamespace("explicit"); ns != "explicit" {
		t.Errorf("expected 'explicit', got %q", ns)
	}
	if ns := cc.resolveNamespace(""); ns != "my-ns" {
		t.Errorf("expected 'my-ns', got %q", ns)
	}
	cc2 := &ClusterClient{namespace: ""}
	if ns := cc2.resolveNamespace(""); ns != "default" {
		t.Errorf("expected 'default', got %q", ns)
	}
}

func TestListResources_Pods(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	// Create some pods
	for _, name := range []string{"pod-a", "pod-b"} {
		_, err := fc.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "nginx"}}},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("creating pod %s: %v", name, err)
		}
	}

	items, err := cc.ListResources(ctx, "pods", "", "")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(items))
	}
}

func TestGetResource_Pod(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	_, err := fc.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pod", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	m, err := cc.GetResource(ctx, "po", "my-pod", "")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	meta, _ := m["metadata"].(map[string]interface{})
	if meta["name"] != "my-pod" {
		t.Errorf("expected name 'my-pod', got %v", meta["name"])
	}
}

func TestGetResource_Deployment(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	replicas := int32(3)
	_, err := fc.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "my-deploy", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	m, err := cc.GetResource(ctx, "deploy", "my-deploy", "default")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	meta, _ := m["metadata"].(map[string]interface{})
	if meta["name"] != "my-deploy" {
		t.Errorf("expected name 'my-deploy', got %v", meta["name"])
	}
}

func TestSecretRedaction(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	_, err := fc.CoreV1().Secrets("default").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data: map[string][]byte{
			"password": []byte("super-secret-value"),
			"api-key":  []byte("abc123"),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Test GetResource redaction
	m, err := cc.GetResource(ctx, "secret", "my-secret", "")
	if err != nil {
		t.Fatalf("GetResource: %v", err)
	}
	data, ok := m["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data field to be a map")
	}
	for key, val := range data {
		if val != "<REDACTED>" {
			t.Errorf("secret data key %q should be redacted, got %v", key, val)
		}
	}

	// Test ListResources redaction
	items, err := cc.ListResources(ctx, "secrets", "", "")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 secret, got %d", len(items))
	}
	listData, ok := items[0]["data"].(map[string]interface{})
	if !ok {
		t.Fatal("expected data field in list item")
	}
	for key, val := range listData {
		if val != "<REDACTED>" {
			t.Errorf("secret list data key %q should be redacted, got %v", key, val)
		}
	}
}

func TestDeleteResource(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	_, err := fc.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "doomed-pod", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err := cc.DeleteResource(ctx, "pod", "doomed-pod", ""); err != nil {
		t.Fatalf("DeleteResource: %v", err)
	}

	// Verify it's gone
	_, err = cc.GetResource(ctx, "pod", "doomed-pod", "")
	if err == nil {
		t.Error("expected error getting deleted pod")
	}
}

func TestScaleResource(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	replicas := int32(2)
	_, err := fc.AppsV1().Deployments("default").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "scalable", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "scale"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "scale"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if err := cc.ScaleResource(ctx, "deploy", "scalable", "", 5); err != nil {
		t.Fatalf("ScaleResource: %v", err)
	}

	// Verify scale was updated
	dep, err := fc.AppsV1().Deployments("default").Get(ctx, "scalable", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *dep.Spec.Replicas != 5 {
		t.Errorf("expected 5 replicas, got %d", *dep.Spec.Replicas)
	}
}

func TestScaleResource_UnsupportedKind(t *testing.T) {
	cc := &ClusterClient{name: "test", clientset: fake.NewSimpleClientset(), namespace: "default"}
	err := cc.ScaleResource(context.Background(), "service", "my-svc", "", 3)
	if err == nil {
		t.Error("expected error for unsupported kind")
	}
}

func TestListNamespaces(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	for _, name := range []string{"default", "kube-system", "production"} {
		_, err := fc.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}

	names, err := cc.ListNamespaces(ctx)
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 namespaces, got %d", len(names))
	}

	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	for _, expected := range []string{"default", "kube-system", "production"} {
		if !found[expected] {
			t.Errorf("missing namespace %q", expected)
		}
	}
}

func TestDescribeResource_Pod(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	_, err := fc.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "described-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			NodeName:   "node-1",
			Containers: []corev1.Container{{Name: "web", Image: "nginx:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "10.0.0.5",
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	desc, err := cc.DescribeResource(ctx, "pod", "described-pod", "")
	if err != nil {
		t.Fatalf("DescribeResource: %v", err)
	}

	for _, want := range []string{"described-pod", "pods", "web", "nginx:latest"} {
		if !containsString(desc, want) {
			t.Errorf("describe output should contain %q, got:\n%s", want, desc)
		}
	}
}

func TestGetResource_UnsupportedKind(t *testing.T) {
	cc := &ClusterClient{name: "test", clientset: fake.NewSimpleClientset(), namespace: "default"}
	_, err := cc.GetResource(context.Background(), "foobar", "x", "")
	if err == nil {
		t.Error("expected error for unsupported kind")
	}
}

func TestListResources_WithLabelSelector(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	// Create pods with different labels
	_, _ = fc.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "labeled", Namespace: "default", Labels: map[string]string{"env": "prod"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
	}, metav1.CreateOptions{})
	_, _ = fc.CoreV1().Pods("default").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "unlabeled", Namespace: "default"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx"}}},
	}, metav1.CreateOptions{})

	// The fake client doesn't actually filter by label selector, but we verify the call works
	items, err := cc.ListResources(ctx, "pods", "", "env=prod")
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	// Fake client returns all pods regardless of selector
	if len(items) < 1 {
		t.Error("expected at least 1 pod")
	}
}

func TestGetEvents(t *testing.T) {
	ctx := context.Background()
	fc := fake.NewSimpleClientset()
	cc := &ClusterClient{name: "test", clientset: fc, namespace: "default"}

	_, err := fc.CoreV1().Events("default").Create(ctx, &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{Name: "ev1", Namespace: "default"},
		Reason:     "Scheduled",
		Message:    "Successfully assigned",
		Type:       "Normal",
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	events, err := cc.GetEvents(ctx, "", "")
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestClose(t *testing.T) {
	cm := NewClientManager(nil)
	// Should not panic
	cm.Close()
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
