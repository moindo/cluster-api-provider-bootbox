// Package controller contains the BootBoxHost and BootBoxMachine reconcilers.
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
	"github.com/moindo/cluster-api-provider-bootbox/internal/redfish"
)

// HostReconciler keeps the inventory data of BootBoxHosts current.
type HostReconciler struct {
	Client          client.Client
	BMC             redfish.Factory
	InspectInterval time.Duration
	Now             func() time.Time
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootboxhosts,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootboxhosts/status;bootboxhosts/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile inspects the host through its BMC.
func (r *HostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	host := &infrav1.BootBoxHost{}
	if err := r.Client.Get(ctx, req.NamespacedName, host); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !host.DeletionTimestamp.IsZero() {
		if host.Status.ConsumerRef != nil {
			logger.Info("host is claimed, delaying deletion", "consumer", host.Status.ConsumerRef.Name)
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		if controllerutil.RemoveFinalizer(host, infrav1.HostFinalizer) {
			return ctrl.Result{}, r.Client.Update(ctx, host)
		}
		return ctrl.Result{}, nil
	}
	if controllerutil.AddFinalizer(host, infrav1.HostFinalizer) {
		if err := r.Client.Update(ctx, host); err != nil {
			return ctrl.Result{}, err
		}
	}

	base := host.DeepCopy()
	if host.Status.State == "" {
		host.Status.State = infrav1.HostStateAvailable
	}

	due := host.Status.LastInspected == nil ||
		r.now().Sub(host.Status.LastInspected.Time) >= r.InspectInterval ||
		!meta.IsStatusConditionTrue(host.Status.Conditions, infrav1.ConditionBMCReachable)
	if due {
		r.inspect(ctx, host)
	}

	if err := r.Client.Status().Patch(ctx, host, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.InspectInterval}, nil
}

func (r *HostReconciler) inspect(ctx context.Context, host *infrav1.BootBoxHost) {
	bmc, err := bmcFor(ctx, r.Client, r.BMC, host)
	var info redfish.Info
	if err == nil {
		info, err = bmc.Inspect(ctx)
	}
	if err != nil {
		log.FromContext(ctx).Info("BMC inspection failed", "error", err.Error())
		setBMCCondition(host, err)
		return
	}
	now := metav1.NewTime(r.now())
	host.Status.LastInspected = &now
	host.Status.Hardware = infrav1.HardwareInfo{
		Manufacturer: info.Manufacturer,
		Model:        info.Model,
		SerialNumber: info.SerialNumber,
		BIOSVersion:  info.BIOSVersion,
		PowerState:   info.PowerState,
	}
	setBMCCondition(host, nil)
}

func setBMCCondition(host *infrav1.BootBoxHost, err error) {
	c := metav1.Condition{
		Type: infrav1.ConditionBMCReachable, Status: metav1.ConditionTrue,
		Reason: "Reachable", Message: "Redfish call succeeded", ObservedGeneration: host.Generation,
	}
	if err != nil {
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, "RedfishError", err.Error()
	}
	meta.SetStatusCondition(&host.Status.Conditions, c)
}

func (r *HostReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// bmcFor builds a BMC client from the host spec and its credentials Secret.
func bmcFor(ctx context.Context, c client.Reader, f redfish.Factory, host *infrav1.BootBoxHost) (redfish.BMC, error) {
	s := &corev1.Secret{}
	key := client.ObjectKey{Namespace: host.Namespace, Name: host.Spec.BMC.CredentialsName}
	if err := c.Get(ctx, key, s); err != nil {
		return nil, fmt.Errorf("BMC credentials %s: %w", key.Name, err)
	}
	return f(redfish.Config{
		Address:            host.Spec.BMC.Address,
		Username:           string(s.Data["username"]),
		Password:           string(s.Data["password"]),
		Fingerprint:        host.Spec.BMC.CertificateFingerprint,
		InsecureSkipVerify: host.Spec.BMC.InsecureSkipVerify,
		SystemID:           host.Spec.BMC.SystemID,
	})
}

// SetupWithManager registers the reconciler.
func (r *HostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&infrav1.BootBoxHost{}).Named("bootboxhost").Complete(r)
}
