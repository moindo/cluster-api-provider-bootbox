package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	v1beta2conditions "sigs.k8s.io/cluster-api/util/conditions/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
)

// v1beta2ConditionTypes are the v1beta2 conditions the machine controller owns.
var v1beta2ConditionTypes = []string{
	infrav1.ReadyV1Beta2Condition,
	infrav1.HostClaimedV1Beta2Condition,
	infrav1.BMCReachableV1Beta2Condition,
	infrav1.HostBootedV1Beta2Condition,
	infrav1.MachineConfigServedV1Beta2Condition,
	infrav1.HostProvisionedV1Beta2Condition,
}

// readyStepTypes make up Ready, which matches status.ready: the
// infrastructure is ready once the host fetched its config, the node joins
// on its own.
var readyStepTypes = []string{
	infrav1.HostClaimedV1Beta2Condition,
	infrav1.HostBootedV1Beta2Condition,
	infrav1.MachineConfigServedV1Beta2Condition,
}

// setStepConditions derives the v1beta2 step conditions and the v1beta1
// Ready condition from the claimed host and the v1beta1 HostProvisioned
// condition, so that `kubectl describe`, clusterctl and the Machine's
// InfrastructureReady show where provisioning stands. host may be nil.
func (r *MachineReconciler) setStepConditions(bbm *infrav1.BootBoxMachine, host *infrav1.BootBoxHost) {
	hp := conditions.Get(bbm, infrav1.HostProvisionedCondition)
	set := func(t string, status metav1.ConditionStatus, reason, msg string, args ...any) {
		v1beta2conditions.Set(bbm, metav1.Condition{
			Type: t, Status: status, Reason: reason, Message: fmt.Sprintf(msg, args...),
			ObservedGeneration: bbm.Generation,
		})
	}
	provisioned := host != nil && host.Status.State == infrav1.HostStateProvisioned

	// HostClaimed
	switch {
	case host != nil:
		set(infrav1.HostClaimedV1Beta2Condition, metav1.ConditionTrue, infrav1.ClaimedV1Beta2Reason,
			"BootBoxHost %s/%s", host.Namespace, host.Name)
	case hp != nil && hp.Reason != "":
		set(infrav1.HostClaimedV1Beta2Condition, metav1.ConditionFalse, hp.Reason, "%s", hp.Message)
	default:
		set(infrav1.HostClaimedV1Beta2Condition, metav1.ConditionFalse, infrav1.NotClaimedV1Beta2Reason, "")
	}

	// BMCReachable, mirrored from the host.
	if host == nil {
		set(infrav1.BMCReachableV1Beta2Condition, metav1.ConditionUnknown, infrav1.NoHostV1Beta2Reason, "no host claimed")
	} else if c := meta.FindStatusCondition(host.Status.Conditions, infrav1.ConditionBMCReachable); c != nil {
		set(infrav1.BMCReachableV1Beta2Condition, c.Status, c.Reason, "%s", c.Message)
	} else {
		set(infrav1.BMCReachableV1Beta2Condition, metav1.ConditionUnknown, infrav1.ConditionUnknownV1Beta2Reason, "host %s was not inspected yet", host.Name)
	}

	var p infrav1.ProvisioningStatus
	if host != nil {
		p = host.Status.Provisioning
	}
	bootFailed := hp != nil && hp.Status != corev1.ConditionTrue && hp.Reason == infrav1.BootFailedReason

	// HostBooted
	switch {
	case p.IPXEServedAt != nil:
		set(infrav1.HostBootedV1Beta2Condition, metav1.ConditionTrue, infrav1.IPXEServedV1Beta2Reason,
			"iPXE script fetched at %s (boot attempt %d)", stamp(p.IPXEServedAt), p.BootAttempts)
	case provisioned:
		set(infrav1.HostBootedV1Beta2Condition, metav1.ConditionTrue, infrav1.ProvisionedV1Beta2Reason, "")
	case bootFailed && p.ConfigServedAt == nil:
		set(infrav1.HostBootedV1Beta2Condition, metav1.ConditionFalse, infrav1.BootFailedReason, "%s", hp.Message)
	case p.BootRequestedAt != nil:
		set(infrav1.HostBootedV1Beta2Condition, metav1.ConditionFalse, infrav1.BootRequestedV1Beta2Reason,
			"PXE boot requested at %s (attempt %d of %d), waiting for the iPXE fetch",
			stamp(p.BootRequestedAt), p.BootAttempts, r.MaxBootAttempts)
	default:
		set(infrav1.HostBootedV1Beta2Condition, metav1.ConditionFalse, infrav1.BootNotRequestedV1Beta2Reason, "")
	}

	// MachineConfigServed
	switch {
	case p.ConfigServedAt != nil:
		set(infrav1.MachineConfigServedV1Beta2Condition, metav1.ConditionTrue, infrav1.ConfigServedV1Beta2Reason,
			"machine config fetched at %s", stamp(p.ConfigServedAt))
	case provisioned:
		set(infrav1.MachineConfigServedV1Beta2Condition, metav1.ConditionTrue, infrav1.ProvisionedV1Beta2Reason, "")
	case p.IPXEServedAt != nil:
		set(infrav1.MachineConfigServedV1Beta2Condition, metav1.ConditionFalse, infrav1.WaitingForConfigFetchReason,
			"Talos is installing; the config fetch follows its boot")
	default:
		set(infrav1.MachineConfigServedV1Beta2Condition, metav1.ConditionFalse, infrav1.WaitingForConfigFetchReason, "")
	}

	// HostProvisioned mirrors the v1beta1 condition.
	switch {
	case hp == nil:
		set(infrav1.HostProvisionedV1Beta2Condition, metav1.ConditionUnknown, infrav1.ConditionUnknownV1Beta2Reason, "")
	case hp.Status == corev1.ConditionTrue:
		set(infrav1.HostProvisionedV1Beta2Condition, metav1.ConditionTrue, infrav1.ProvisionedV1Beta2Reason, "")
	default:
		set(infrav1.HostProvisionedV1Beta2Condition, metav1.ConditionStatus(hp.Status), hp.Reason, "%s", hp.Message)
	}

	// Ready
	if err := v1beta2conditions.SetSummaryCondition(bbm, bbm, infrav1.ReadyV1Beta2Condition,
		v1beta2conditions.ForConditionTypes(readyStepTypes)); err != nil {
		set(infrav1.ReadyV1Beta2Condition, metav1.ConditionUnknown, infrav1.ConditionUnknownV1Beta2Reason, "%s", err.Error())
	}

	// v1beta1 Ready carries the same status for consumers of status.conditions.
	ready := v1beta2conditions.Get(bbm, infrav1.ReadyV1Beta2Condition)
	if ready.Status == metav1.ConditionTrue {
		conditions.MarkTrue(bbm, clusterv1.ReadyCondition)
		return
	}
	severity := clusterv1.ConditionSeverityInfo
	if hp != nil && hp.Status != corev1.ConditionTrue && hp.Severity != "" {
		severity = hp.Severity
	}
	reason := ready.Reason
	if hp != nil && hp.Status != corev1.ConditionTrue && hp.Reason != "" {
		reason = hp.Reason
	}
	conditions.MarkFalse(bbm, clusterv1.ReadyCondition, reason, severity, "%s", ready.Message)
}

// hostOf returns the claimed host, nil when none is claimed or it is gone.
func (r *MachineReconciler) hostOf(ctx context.Context, bbm *infrav1.BootBoxMachine) *infrav1.BootBoxHost {
	if bbm.Status.HostRef == nil {
		return nil
	}
	host := &infrav1.BootBoxHost{}
	key := client.ObjectKey{Namespace: bbm.Status.HostRef.Namespace, Name: bbm.Status.HostRef.Name}
	if err := r.Client.Get(ctx, key, host); err != nil {
		return nil
	}
	if host.Status.ConsumerRef == nil || host.Status.ConsumerRef.UID != bbm.UID {
		return nil
	}
	return host
}

func stamp(t *metav1.Time) string { return t.UTC().Format(time.RFC3339) }
