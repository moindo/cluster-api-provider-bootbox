// Package statuscm publishes per-host status summaries into ConfigMaps that
// dashboards can display (the Cozystack console shows ConfigMaps listed in an
// application's resource map). Objects opt in with the annotation
// StatusConfigMapAnnotation = "<namespace>/<name>"; each host or machine owns
// one key of that ConfigMap.
package statuscm

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// StatusConfigMapAnnotation names the ConfigMap ("<namespace>/<name>") an
// object reports its status into.
const StatusConfigMapAnnotation = "bootbox.infrastructure.cluster.x-k8s.io/status-configmap"

// managedByLabel marks ConfigMaps this package created.
const managedByLabel = "app.kubernetes.io/managed-by"

// Target parses the annotation of obj; ok is false when it is absent.
func Target(obj client.Object) (key client.ObjectKey, ok bool, err error) {
	v := obj.GetAnnotations()[StatusConfigMapAnnotation]
	if v == "" {
		return key, false, nil
	}
	ns, name, found := strings.Cut(v, "/")
	if !found || ns == "" || name == "" {
		return key, false, fmt.Errorf("annotation %s=%q: want <namespace>/<name>", StatusConfigMapAnnotation, v)
	}
	return client.ObjectKey{Namespace: ns, Name: name}, true, nil
}

// Set writes entry (marshalled to YAML) under field; it creates the
// ConfigMap when needed and only writes when the value changed.
func Set(ctx context.Context, c client.Client, key client.ObjectKey, field string, entry any) error {
	b, err := yaml.Marshal(entry)
	if err != nil {
		return err
	}
	value := string(b)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm := &corev1.ConfigMap{}
		err := c.Get(ctx, key, cm)
		if apierrors.IsNotFound(err) {
			cm = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: key.Namespace, Name: key.Name,
					Labels: map[string]string{managedByLabel: "cluster-api-provider-bootbox"},
				},
				Data: map[string]string{field: value},
			}
			return c.Create(ctx, cm)
		}
		if err != nil {
			return err
		}
		if cm.Data[field] == value {
			return nil
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[field] = value
		return c.Update(ctx, cm)
	})
}

// Remove deletes field; a missing ConfigMap or field is not an error.
func Remove(ctx context.Context, c client.Client, key client.ObjectKey, field string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm := &corev1.ConfigMap{}
		if err := c.Get(ctx, key, cm); err != nil {
			return client.IgnoreNotFound(err)
		}
		if _, ok := cm.Data[field]; !ok {
			return nil
		}
		delete(cm.Data, field)
		return c.Update(ctx, cm)
	})
}
