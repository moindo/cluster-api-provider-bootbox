// Package appevents records lifecycle events against the Helm release that
// owns a pool or a host, so that dashboards listing the events of an
// application (the Cozystack console filters events by the release name) show
// the bare-metal lifecycle next to the app.
package appevents

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ReleaseNameLabel and ReleaseNamespaceLabel are set by Flux on every
	// object of a HelmRelease.
	ReleaseNameLabel      = "helm.toolkit.fluxcd.io/name"
	ReleaseNamespaceLabel = "helm.toolkit.fluxcd.io/namespace"

	component = "cluster-api-provider-bootbox"
)

// Target is the HelmRelease an event is recorded against.
type Target struct {
	Namespace string
	Name      string
}

// TargetOf returns the release that owns obj, from the Flux labels.
func TargetOf(obj client.Object) (Target, bool) {
	l := obj.GetLabels()
	t := Target{Namespace: l[ReleaseNamespaceLabel], Name: l[ReleaseNameLabel]}
	return t, t.Namespace != "" && t.Name != ""
}

// Record creates a core/v1 Event for the release. Callers record transitions
// only, so events are not aggregated.
func Record(ctx context.Context, c client.Client, t Target, eventType, reason, message string, now time.Time) error {
	ts := metav1.NewTime(now)
	ev := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:    t.Namespace,
			GenerateName: t.Name + ".",
		},
		InvolvedObject: corev1.ObjectReference{
			APIVersion: "helm.toolkit.fluxcd.io/v2",
			Kind:       "HelmRelease",
			Namespace:  t.Namespace,
			Name:       t.Name,
		},
		Reason:              reason,
		Message:             message,
		Type:                eventType,
		Source:              corev1.EventSource{Component: component},
		ReportingController: "infrastructure.cluster.x-k8s.io/" + component,
		ReportingInstance:   component,
		FirstTimestamp:      ts,
		LastTimestamp:       ts,
		Count:               1,
	}
	return c.Create(ctx, ev)
}
