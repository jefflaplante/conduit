//go:build with_k8s

package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ---------- Helper: object to map ----------

func toMap(obj interface{}) (map[string]interface{}, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func toMapSlice(obj interface{}) ([]map[string]interface{}, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, raw := range items {
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, nil
}

// ---------- Secret redaction ----------

func redactSecretData(m map[string]interface{}) {
	if data, ok := m["data"].(map[string]interface{}); ok {
		for k := range data {
			data[k] = "<REDACTED>"
		}
	}
}

// ---------- ClusterClient resource methods ----------

// GetResource retrieves a single resource by kind, name, and namespace.
func (cc *ClusterClient) GetResource(ctx context.Context, kind, name, namespace string) (map[string]interface{}, error) {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)
	opts := metav1.GetOptions{}

	var obj interface{}
	var err error

	switch normalized {
	case "pods":
		obj, err = cc.clientset.CoreV1().Pods(ns).Get(ctx, name, opts)
	case "deployments":
		obj, err = cc.clientset.AppsV1().Deployments(ns).Get(ctx, name, opts)
	case "services":
		obj, err = cc.clientset.CoreV1().Services(ns).Get(ctx, name, opts)
	case "configmaps":
		obj, err = cc.clientset.CoreV1().ConfigMaps(ns).Get(ctx, name, opts)
	case "secrets":
		obj, err = cc.clientset.CoreV1().Secrets(ns).Get(ctx, name, opts)
	case "nodes":
		obj, err = cc.clientset.CoreV1().Nodes().Get(ctx, name, opts)
	case "namespaces":
		obj, err = cc.clientset.CoreV1().Namespaces().Get(ctx, name, opts)
	case "statefulsets":
		obj, err = cc.clientset.AppsV1().StatefulSets(ns).Get(ctx, name, opts)
	case "daemonsets":
		obj, err = cc.clientset.AppsV1().DaemonSets(ns).Get(ctx, name, opts)
	case "jobs":
		obj, err = cc.clientset.BatchV1().Jobs(ns).Get(ctx, name, opts)
	case "cronjobs":
		obj, err = cc.clientset.BatchV1().CronJobs(ns).Get(ctx, name, opts)
	case "ingresses":
		obj, err = cc.clientset.NetworkingV1().Ingresses(ns).Get(ctx, name, opts)
	case "persistentvolumeclaims":
		obj, err = cc.clientset.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, opts)
	case "events":
		obj, err = cc.clientset.CoreV1().Events(ns).Get(ctx, name, opts)
	case "replicasets":
		obj, err = cc.clientset.AppsV1().ReplicaSets(ns).Get(ctx, name, opts)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
	if err != nil {
		return nil, err
	}

	m, err := toMap(obj)
	if err != nil {
		return nil, err
	}

	if normalized == "secrets" {
		redactSecretData(m)
	}
	return m, nil
}

// ListResources lists resources of a given kind with an optional label selector.
func (cc *ClusterClient) ListResources(ctx context.Context, kind, namespace, labelSelector string) ([]map[string]interface{}, error) {
	ns := cc.resolveNamespace(namespace)
	normalized := normalizeKind(kind)
	opts := metav1.ListOptions{LabelSelector: labelSelector}

	var items interface{}
	var err error

	switch normalized {
	case "pods":
		list, e := cc.clientset.CoreV1().Pods(ns).List(ctx, opts)
		err, items = e, podItems(list)
	case "deployments":
		list, e := cc.clientset.AppsV1().Deployments(ns).List(ctx, opts)
		err, items = e, deploymentItems(list)
	case "services":
		list, e := cc.clientset.CoreV1().Services(ns).List(ctx, opts)
		err, items = e, serviceItems(list)
	case "configmaps":
		list, e := cc.clientset.CoreV1().ConfigMaps(ns).List(ctx, opts)
		err, items = e, configMapItems(list)
	case "secrets":
		list, e := cc.clientset.CoreV1().Secrets(ns).List(ctx, opts)
		err, items = e, secretItems(list)
	case "nodes":
		list, e := cc.clientset.CoreV1().Nodes().List(ctx, opts)
		err, items = e, nodeItems(list)
	case "namespaces":
		list, e := cc.clientset.CoreV1().Namespaces().List(ctx, opts)
		err, items = e, namespaceItems(list)
	case "statefulsets":
		list, e := cc.clientset.AppsV1().StatefulSets(ns).List(ctx, opts)
		err, items = e, statefulSetItems(list)
	case "daemonsets":
		list, e := cc.clientset.AppsV1().DaemonSets(ns).List(ctx, opts)
		err, items = e, daemonSetItems(list)
	case "jobs":
		list, e := cc.clientset.BatchV1().Jobs(ns).List(ctx, opts)
		err, items = e, jobItems(list)
	case "cronjobs":
		list, e := cc.clientset.BatchV1().CronJobs(ns).List(ctx, opts)
		err, items = e, cronJobItems(list)
	case "ingresses":
		list, e := cc.clientset.NetworkingV1().Ingresses(ns).List(ctx, opts)
		err, items = e, ingressItems(list)
	case "persistentvolumeclaims":
		list, e := cc.clientset.CoreV1().PersistentVolumeClaims(ns).List(ctx, opts)
		err, items = e, pvcItems(list)
	case "events":
		list, e := cc.clientset.CoreV1().Events(ns).List(ctx, opts)
		err, items = e, eventItems(list)
	case "replicasets":
		list, e := cc.clientset.AppsV1().ReplicaSets(ns).List(ctx, opts)
		err, items = e, replicaSetItems(list)
	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", kind)
	}
	if err != nil {
		return nil, err
	}

	result, err := toMapSlice(items)
	if err != nil {
		return nil, err
	}

	if normalized == "secrets" {
		for i := range result {
			redactSecretData(result[i])
		}
	}
	return result, nil
}

// List item extractors — these pull the .Items slice from typed list objects
// while handling the nil list case that the fake client can return.

func podItems(l *corev1.PodList) []corev1.Pod {
	if l == nil {
		return nil
	}
	return l.Items
}

func deploymentItems(l *appsv1.DeploymentList) []appsv1.Deployment {
	if l == nil {
		return nil
	}
	return l.Items
}

func serviceItems(l *corev1.ServiceList) []corev1.Service {
	if l == nil {
		return nil
	}
	return l.Items
}

func configMapItems(l *corev1.ConfigMapList) []corev1.ConfigMap {
	if l == nil {
		return nil
	}
	return l.Items
}

func secretItems(l *corev1.SecretList) []corev1.Secret {
	if l == nil {
		return nil
	}
	return l.Items
}

func nodeItems(l *corev1.NodeList) []corev1.Node {
	if l == nil {
		return nil
	}
	return l.Items
}

func namespaceItems(l *corev1.NamespaceList) []corev1.Namespace {
	if l == nil {
		return nil
	}
	return l.Items
}

func statefulSetItems(l *appsv1.StatefulSetList) []appsv1.StatefulSet {
	if l == nil {
		return nil
	}
	return l.Items
}

func daemonSetItems(l *appsv1.DaemonSetList) []appsv1.DaemonSet {
	if l == nil {
		return nil
	}
	return l.Items
}

func jobItems(l *batchv1.JobList) []batchv1.Job {
	if l == nil {
		return nil
	}
	return l.Items
}

func cronJobItems(l *batchv1.CronJobList) []batchv1.CronJob {
	if l == nil {
		return nil
	}
	return l.Items
}

func ingressItems(l *networkingv1.IngressList) []networkingv1.Ingress {
	if l == nil {
		return nil
	}
	return l.Items
}

func pvcItems(l *corev1.PersistentVolumeClaimList) []corev1.PersistentVolumeClaim {
	if l == nil {
		return nil
	}
	return l.Items
}

func eventItems(l *corev1.EventList) []corev1.Event {
	if l == nil {
		return nil
	}
	return l.Items
}

func replicaSetItems(l *appsv1.ReplicaSetList) []appsv1.ReplicaSet {
	if l == nil {
		return nil
	}
	return l.Items
}
