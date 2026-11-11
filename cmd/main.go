// Command manager runs the BootBox infrastructure provider: the BootBoxHost
// and BootBoxMachine controllers and the provisioning boot server.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
	"github.com/moindo/cluster-api-provider-bootbox/internal/bootserver"
	"github.com/moindo/cluster-api-provider-bootbox/internal/controller"
	"github.com/moindo/cluster-api-provider-bootbox/internal/provision"
	"github.com/moindo/cluster-api-provider-bootbox/internal/redfish"
)

// Kernel arguments of the BootBox matchbox "default" profile.
const defaultKernelArgs = "initrd=initramfs.xz,init_on_alloc=1,slab_nomerge,pti=on,console=tty0,console=ttyS0," +
	"printk.devkmsg=on,talos.platform=metal"

// The first boot of a provisioning wipes the system disk, so a reused host
// never boots its previous STATE. Talos reboots after the wipe.
const defaultWipeArgs = "talos.experimental.wipe=system"

func main() {
	var (
		metricsAddr, probeAddr, bootAddr string
		leaderElect                      bool
		hostNamespace, bootBaseURL       string
		kernelURL, initrdURL, kernelArgs string
		wipeArgs                         string
		idleIPXEURL                      string
		bootTimeout, inspectInterval     time.Duration
		maxBootAttempts                  int
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Health probe endpoint.")
	flag.StringVar(&bootAddr, "boot-bind-address", ":8082", "Listen address of the boot server (iPXE script and machine config).")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&hostNamespace, "host-namespace", "cozy-bootbox", "Namespace of BootBoxHosts and Tinkerbell Hardware.")
	flag.StringVar(&bootBaseURL, "boot-base-url", "", "External URL of the boot server as seen by booting hosts, e.g. http://pxe.example.com/capi.")
	flag.StringVar(&kernelURL, "kernel-url", "", "URL of the Talos PXE kernel.")
	flag.StringVar(&initrdURL, "initrd-url", "", "URL of the Talos PXE initramfs.")
	flag.StringVar(&kernelArgs, "kernel-args", defaultKernelArgs, "Comma-separated kernel arguments of the provisioning boot.")
	flag.StringVar(&wipeArgs, "wipe-kernel-args", defaultWipeArgs, "Comma-separated kernel arguments added to the first boot of a provisioning only.")
	flag.StringVar(&idleIPXEURL, "idle-ipxe-url", "", "iPXE URL restored on the Hardware when a host is not provisioning.")
	flag.DurationVar(&bootTimeout, "boot-timeout", 15*time.Minute, "Time to wait for the config fetch before booting again.")
	flag.IntVar(&maxBootAttempts, "max-boot-attempts", 3, "PXE boot requests per provisioning.")
	flag.DurationVar(&inspectInterval, "inspect-interval", 10*time.Minute, "Interval of BMC inspections.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logger := ctrl.Log.WithName("setup")

	base, err := url.Parse(bootBaseURL)
	if bootBaseURL == "" || err != nil || base.Host == "" {
		logger.Error(err, "--boot-base-url must be an absolute URL")
		os.Exit(1)
	}
	if kernelURL == "" || initrdURL == "" {
		logger.Error(errors.New("missing flag"), "--kernel-url and --initrd-url are required")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, clusterv1.AddToScheme, infrav1.AddToScheme} {
		if err := add(scheme); err != nil {
			logger.Error(err, "building scheme")
			os.Exit(1)
		}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "cluster-api-provider-bootbox",
		// Secrets are read directly: caching them would mirror every Secret
		// of the cluster (bootstrap data lives in the tenant namespaces).
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}}}},
	})
	if err != nil {
		logger.Error(err, "creating manager")
		os.Exit(1)
	}

	urls := provision.BootURLs{Base: base}
	if err := (&controller.HostReconciler{
		Client: mgr.GetClient(), BMC: redfish.New, InspectInterval: inspectInterval,
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "setting up BootBoxHost controller")
		os.Exit(1)
	}
	if err := (&controller.MachineReconciler{
		Client: mgr.GetClient(), BMC: redfish.New, URLs: urls,
		HostNamespace: hostNamespace, IdleIPXEURL: idleIPXEURL,
		BootTimeout: bootTimeout, MaxBootAttempts: int32(maxBootAttempts), //nolint:gosec // small flag value
	}).SetupWithManager(mgr); err != nil {
		logger.Error(err, "setting up BootBoxMachine controller")
		os.Exit(1)
	}

	boot := &bootserver.Server{
		Client: mgr.GetClient(), URLs: urls, HostNamespace: hostNamespace,
		KernelURL: kernelURL, InitrdURL: initrdURL, KernelArgs: splitArgs(kernelArgs), WipeArgs: splitArgs(wipeArgs),
	}
	if err := mgr.Add(&httpRunnable{addr: bootAddr, handler: boot.Handler()}); err != nil {
		logger.Error(err, "adding boot server")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		os.Exit(1)
	}
	logger.Info("starting manager", "bootBaseURL", base.String(), "hostNamespace", hostNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "manager exited")
		os.Exit(1)
	}
}

func splitArgs(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// httpRunnable runs the boot server inside the manager. It does not need
// leadership: a booting host must get an answer from any replica.
type httpRunnable struct {
	addr    string
	handler http.Handler
}

func (h *httpRunnable) NeedLeaderElection() bool { return false }

func (h *httpRunnable) Start(ctx context.Context) error {
	srv := &http.Server{Addr: h.addr, Handler: h.handler, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
