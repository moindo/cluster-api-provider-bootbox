package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
	"github.com/moindo/cluster-api-provider-bootbox/internal/hardware"
	"github.com/moindo/cluster-api-provider-bootbox/internal/provision"
	"github.com/moindo/cluster-api-provider-bootbox/internal/redfish"
)

// MachineReconciler claims a BootBoxHost for each BootBoxMachine, PXE-boots
// it into the provisioning flow and releases it on deletion.
type MachineReconciler struct {
	Client client.Client
	BMC    redfish.Factory
	URLs   provision.BootURLs
	// HostNamespace is where BootBoxHosts and Hardware objects live.
	HostNamespace string
	// IdleIPXEURL is restored on the Hardware when a host is released.
	IdleIPXEURL string
	// BootTimeout is how long to wait for the config fetch before the PXE
	// boot is requested again, up to MaxBootAttempts times.
	BootTimeout     time.Duration
	MaxBootAttempts int32
	Now             func() time.Time
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootboxmachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootboxmachines/status;bootboxmachines/finalizers,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootboxmachinetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=tinkerbell.org,resources=hardware,verbs=get;list;watch;update;patch

// Reconcile drives one BootBoxMachine.
func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)
	bbm := &infrav1.BootBoxMachine{}
	if err := r.Client.Get(ctx, req.NamespacedName, bbm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, bbm.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		logger.Info("waiting for the Machine controller to set the owner reference")
		return ctrl.Result{}, nil
	}
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		logger.Info("machine is not associated with a cluster yet")
		return ctrl.Result{}, nil
	}
	if annotations.IsPaused(cluster, bbm) {
		logger.Info("reconciliation is paused")
		return ctrl.Result{}, nil
	}

	helper, err := patch.NewHelper(bbm, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := helper.Patch(ctx, bbm, patch.WithOwnedConditions{Conditions: []clusterv1.ConditionType{
			infrav1.HostProvisionedCondition,
		}}); err != nil {
			reterr = errors.Join(reterr, err)
		}
	}()

	if !bbm.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, bbm)
	}
	if controllerutil.AddFinalizer(bbm, infrav1.MachineFinalizer) {
		return ctrl.Result{Requeue: true}, nil
	}
	return r.reconcileNormal(ctx, machine, bbm)
}

func (r *MachineReconciler) reconcileNormal(ctx context.Context, machine *clusterv1.Machine, bbm *infrav1.BootBoxMachine) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if machine.Spec.Bootstrap.DataSecretName == nil && bbm.Status.HostRef == nil {
		conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, infrav1.WaitingForBootstrapDataReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	host, err := r.claim(ctx, bbm)
	if err != nil {
		return ctrl.Result{}, err
	}
	if host == nil {
		conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, infrav1.NoHostAvailableReason, clusterv1.ConditionSeverityWarning,
			"no available BootBoxHost in %s matches the selector and admits namespace %s", r.HostNamespace, bbm.Namespace)
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	bbm.Status.HostRef = &infrav1.HostReference{Namespace: host.Namespace, Name: host.Name}
	logger = logger.WithValues("host", host.Name)

	hw, err := hardware.Get(ctx, r.Client, host.Namespace, host.Spec.HardwareName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("hardware %s: %w", host.Spec.HardwareName, err)
	}
	res, err := hardware.ReservationOf(hw)
	if err != nil {
		return ctrl.Result{}, err
	}
	bbm.Status.Addresses = addresses(host, res)

	switch host.Status.State {
	case infrav1.HostStateProvisioned:
		bbm.Spec.ProviderID = ptr.To(provision.ProviderID(host))
		bbm.Status.Ready = true
		conditions.MarkTrue(bbm, infrav1.HostProvisionedCondition)
		return ctrl.Result{}, nil
	case infrav1.HostStateProvisioning:
	default:
		return ctrl.Result{}, fmt.Errorf("claimed host %s is in unexpected state %q", host.Name, host.Status.State)
	}

	// The host fetched its config: the infrastructure is ready, the node
	// joins on its own. Close the window once CAPI saw the Node.
	if host.Status.Provisioning.ConfigServedAt != nil {
		bbm.Spec.ProviderID = ptr.To(provision.ProviderID(host))
		bbm.Status.Ready = true
		if machine.Status.NodeRef == nil {
			conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, "WaitingForNode", clusterv1.ConditionSeverityInfo,
				"host fetched its config at %s", host.Status.Provisioning.ConfigServedAt.Format(time.RFC3339))
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		if err := r.finishProvisioning(ctx, host); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("host provisioned", "node", machine.Status.NodeRef.Name)
		conditions.MarkTrue(bbm, infrav1.HostProvisionedCondition)
		return ctrl.Result{}, nil
	}

	token, err := provision.EnsureToken(ctx, r.Client, host)
	if err != nil {
		return ctrl.Result{}, err
	}
	ipxeURL, err := r.URLs.IPXE(host, token)
	if err != nil {
		conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, infrav1.BootFailedReason, clusterv1.ConditionSeverityError, "%s", err.Error())
		return ctrl.Result{}, err
	}
	if changed, err := hardware.SetNetboot(hw, ipxeURL, true); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		if err := r.Client.Update(ctx, hw); err != nil {
			return ctrl.Result{}, err
		}
	}

	p := host.Status.Provisioning
	// Count from the latest sign of life: a host that fetched its iPXE
	// script after the boot request is booting and must not be reset.
	since := p.BootRequestedAt
	if p.LastIPXEServedAt != nil && (since == nil || p.LastIPXEServedAt.After(since.Time)) {
		since = p.LastIPXEServedAt
	}
	timedOut := since != nil && r.now().Sub(since.Time) >= r.BootTimeout
	switch {
	case p.BootRequestedAt == nil || (timedOut && p.BootAttempts < r.MaxBootAttempts):
		if err := r.bootHost(ctx, host); err != nil {
			conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, infrav1.BootFailedReason, clusterv1.ConditionSeverityWarning, "%s", err.Error())
			return ctrl.Result{}, err
		}
		logger.Info("requested PXE boot", "attempt", host.Status.Provisioning.BootAttempts)
	case timedOut:
		conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, infrav1.BootFailedReason, clusterv1.ConditionSeverityError,
			"host did not fetch its config after %d boot attempts; check the console and the smee/boot server logs", p.BootAttempts)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, infrav1.WaitingForConfigFetchReason, clusterv1.ConditionSeverityInfo,
		"PXE boot requested (attempt %d)", host.Status.Provisioning.BootAttempts)
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// claim returns the host claimed by bbm, claiming a free one if needed.
func (r *MachineReconciler) claim(ctx context.Context, bbm *infrav1.BootBoxMachine) (*infrav1.BootBoxHost, error) {
	hosts := &infrav1.BootBoxHostList{}
	if err := r.Client.List(ctx, hosts, client.InNamespace(r.HostNamespace)); err != nil {
		return nil, err
	}
	// An earlier reconcile may have claimed a host without persisting HostRef.
	for i := range hosts.Items {
		if ref := hosts.Items[i].Status.ConsumerRef; ref != nil && ref.UID == bbm.UID {
			return &hosts.Items[i], nil
		}
	}
	if bbm.Status.HostRef != nil {
		return nil, fmt.Errorf("host %s/%s is no longer claimed by this machine", bbm.Status.HostRef.Namespace, bbm.Status.HostRef.Name)
	}

	selector, err := metav1.LabelSelectorAsSelector(&bbm.Spec.HostSelector)
	if err != nil {
		return nil, fmt.Errorf("hostSelector: %w", err)
	}
	sort.Slice(hosts.Items, func(i, j int) bool { return hosts.Items[i].Name < hosts.Items[j].Name })
	for i := range hosts.Items {
		h := &hosts.Items[i]
		if !claimable(h, bbm.Namespace) || !selector.Matches(labels.Set(h.Labels)) {
			continue
		}
		h.Status.ConsumerRef = &corev1.ObjectReference{
			APIVersion: infrav1.GroupVersion.String(), Kind: "BootBoxMachine",
			Namespace: bbm.Namespace, Name: bbm.Name, UID: bbm.UID,
		}
		h.Status.State = infrav1.HostStateProvisioning
		h.Status.Provisioning = infrav1.ProvisioningStatus{}
		// Update, not patch: the resourceVersion makes concurrent claims conflict.
		if err := r.Client.Status().Update(ctx, h); err != nil {
			if apierrors.IsConflict(err) {
				continue
			}
			return nil, err
		}
		log.FromContext(ctx).Info("claimed host", "host", h.Name)
		return h, nil
	}
	return nil, nil
}

func claimable(h *infrav1.BootBoxHost, namespace string) bool {
	return h.DeletionTimestamp.IsZero() &&
		h.Status.State == infrav1.HostStateAvailable &&
		h.Status.ConsumerRef == nil &&
		!h.Spec.Maintenance &&
		slices.Contains(h.Spec.ConsumerNamespaces, namespace) &&
		meta.IsStatusConditionTrue(h.Status.Conditions, infrav1.ConditionBMCReachable)
}

// bootHost requests a one-time PXE boot and power-cycles the host.
func (r *MachineReconciler) bootHost(ctx context.Context, host *infrav1.BootBoxHost) error {
	bmc, err := bmcFor(ctx, r.Client, r.BMC, host)
	if err != nil {
		return err
	}
	if err := bmc.PXEBootOnce(ctx); err != nil {
		return fmt.Errorf("setting one-time PXE boot: %w", err)
	}
	if err := bmc.PowerCycle(ctx); err != nil {
		return fmt.Errorf("power cycle: %w", err)
	}
	now := metav1.NewTime(r.now())
	return r.updateHostStatus(ctx, host, func(h *infrav1.BootBoxHost) {
		h.Status.Provisioning.BootRequestedAt = &now
		h.Status.Provisioning.BootAttempts++
	})
}

func (r *MachineReconciler) finishProvisioning(ctx context.Context, host *infrav1.BootBoxHost) error {
	if err := r.idleHardware(ctx, host); err != nil {
		return err
	}
	if err := provision.DeleteToken(ctx, r.Client, host); err != nil {
		return err
	}
	return r.updateHostStatus(ctx, host, func(h *infrav1.BootBoxHost) { h.Status.State = infrav1.HostStateProvisioned })
}

func (r *MachineReconciler) reconcileDelete(ctx context.Context, bbm *infrav1.BootBoxMachine) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if bbm.Status.HostRef != nil {
		host := &infrav1.BootBoxHost{}
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: bbm.Status.HostRef.Namespace, Name: bbm.Status.HostRef.Name}, host)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return ctrl.Result{}, err
		case host.Status.ConsumerRef != nil && host.Status.ConsumerRef.UID == bbm.UID:
			if err := r.release(ctx, host); err != nil {
				conditions.MarkFalse(bbm, infrav1.HostProvisionedCondition, "DeprovisioningFailed", clusterv1.ConditionSeverityWarning, "%s", err.Error())
				return ctrl.Result{}, err
			}
			logger.Info("released host", "host", host.Name)
		}
	}
	controllerutil.RemoveFinalizer(bbm, infrav1.MachineFinalizer)
	return ctrl.Result{}, nil
}

// release powers the host off, closes its PXE window and frees it. The next
// provisioning wipes the disk (talos.experimental.wipe in the kernel args).
func (r *MachineReconciler) release(ctx context.Context, host *infrav1.BootBoxHost) error {
	if err := r.updateHostStatus(ctx, host, func(h *infrav1.BootBoxHost) { h.Status.State = infrav1.HostStateDeprovisioning }); err != nil {
		return err
	}
	bmc, err := bmcFor(ctx, r.Client, r.BMC, host)
	if err != nil {
		return err
	}
	if err := bmc.PowerOff(ctx); err != nil {
		return fmt.Errorf("power off: %w", err)
	}
	if err := r.idleHardware(ctx, host); err != nil {
		return err
	}
	if err := provision.DeleteToken(ctx, r.Client, host); err != nil {
		return err
	}
	return r.updateHostStatus(ctx, host, func(h *infrav1.BootBoxHost) {
		h.Status.State = infrav1.HostStateAvailable
		h.Status.ConsumerRef = nil
		h.Status.Provisioning = infrav1.ProvisioningStatus{}
	})
}

func (r *MachineReconciler) idleHardware(ctx context.Context, host *infrav1.BootBoxHost) error {
	hw, err := hardware.Get(ctx, r.Client, host.Namespace, host.Spec.HardwareName)
	if err != nil {
		return client.IgnoreNotFound(err)
	}
	changed, err := hardware.SetNetboot(hw, r.IdleIPXEURL, false)
	if err != nil || !changed {
		return err
	}
	return r.Client.Update(ctx, hw)
}

func (r *MachineReconciler) updateHostStatus(ctx context.Context, host *infrav1.BootBoxHost, mutate func(*infrav1.BootBoxHost)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &infrav1.BootBoxHost{}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(host), cur); err != nil {
			return err
		}
		mutate(cur)
		if err := r.Client.Status().Update(ctx, cur); err != nil {
			return err
		}
		cur.DeepCopyInto(host)
		return nil
	})
}

func addresses(host *infrav1.BootBoxHost, res hardware.Reservation) []clusterv1.MachineAddress {
	out := []clusterv1.MachineAddress{{Type: clusterv1.MachineHostName, Address: provision.Hostname(host, res)}}
	if res.IP != "" {
		out = append(out, clusterv1.MachineAddress{Type: clusterv1.MachineInternalIP, Address: res.IP})
	}
	return out
}

func (r *MachineReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// SetupWithManager registers the reconciler and its watches.
func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.BootBoxMachine{}).
		Named("bootboxmachine").
		Watches(&clusterv1.Machine{}, handler.EnqueueRequestsFromMapFunc(
			util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("BootBoxMachine")))).
		Watches(&infrav1.BootBoxHost{}, handler.EnqueueRequestsFromMapFunc(hostToMachine)).
		Complete(r)
}

func hostToMachine(_ context.Context, o client.Object) []reconcile.Request {
	h, ok := o.(*infrav1.BootBoxHost)
	if !ok || h.Status.ConsumerRef == nil {
		return nil
	}
	return []reconcile.Request{{NamespacedName: client.ObjectKey{Namespace: h.Status.ConsumerRef.Namespace, Name: h.Status.ConsumerRef.Name}}}
}
