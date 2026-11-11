// Package bootserver serves the per-host iPXE script and Talos machine
// config during provisioning. URLs carry a random per-host token that exists
// only while the host provisions.
package bootserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
	"github.com/moindo/cluster-api-provider-bootbox/internal/hardware"
	"github.com/moindo/cluster-api-provider-bootbox/internal/provision"
	"github.com/moindo/cluster-api-provider-bootbox/internal/talosconfig"
)

// Server answers iPXE and Talos during the provisioning boot.
type Server struct {
	Client client.Client
	URLs   provision.BootURLs
	// HostNamespace is where BootBoxHosts live.
	HostNamespace string
	// KernelURL and InitrdURL point at the Talos PXE assets (for example
	// the BootBox matchbox assets).
	KernelURL  string
	InitrdURL  string
	KernelArgs []string
	// WipeArgs are added on the first boot of a provisioning only, for
	// example talos.experimental.wipe=system. Talos reboots after the wipe
	// and the next boot installs.
	WipeArgs []string
	Now      func() time.Time
}

var ipxeTemplate = template.Must(template.New("ipxe").Parse(`#!ipxe
kernel {{ .Kernel }} {{ .Args }}
initrd {{ .Initrd }}
boot
`))

// Handler returns the HTTP handler, mounted below the base URL's path.
func (s *Server) Handler() http.Handler {
	prefix := strings.TrimSuffix(s.URLs.Base.Path, "/")
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix+"/i/{host}/{token}", s.serveIPXE)
	mux.HandleFunc("GET "+prefix+"/c/{host}/{token}", s.serveConfig)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

var errNotProvisioning = errors.New("host is not provisioning")

// authorize loads the host named in the request and checks its token.
func (s *Server) authorize(r *http.Request) (*infrav1.BootBoxHost, error) {
	h := &infrav1.BootBoxHost{}
	key := client.ObjectKey{Namespace: s.HostNamespace, Name: r.PathValue("host")}
	if err := s.Client.Get(r.Context(), key, h); err != nil {
		return nil, err
	}
	if h.Status.State != infrav1.HostStateProvisioning {
		return nil, errNotProvisioning
	}
	if err := provision.CheckToken(r.Context(), s.Client, h, r.PathValue("token")); err != nil {
		return nil, err
	}
	return h, nil
}

func (s *Server) serveIPXE(w http.ResponseWriter, r *http.Request) {
	logger := log.FromContext(r.Context()).WithValues("host", r.PathValue("host"), "remote", r.RemoteAddr)
	h, err := s.authorize(r)
	if err != nil {
		logger.Info("refusing iPXE request", "reason", err.Error())
		http.NotFound(w, r)
		return
	}
	args := append([]string{}, s.KernelArgs...)
	args = append(args, h.Spec.ExtraKernelArgs...)
	firstBoot := h.Status.Provisioning.IPXEServedAt == nil
	if firstBoot {
		args = append(args, s.WipeArgs...)
	}
	if err := s.updateStatus(r.Context(), h, func(p *infrav1.ProvisioningStatus, now metav1.Time) {
		if p.IPXEServedAt == nil {
			p.IPXEServedAt = &now
		}
		p.LastIPXEServedAt = &now
	}); err != nil {
		logger.Error(err, "recording iPXE fetch")
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	args = append(args, "talos.config="+s.URLs.Config(h, r.PathValue("token")))
	w.Header().Set("Content-Type", "text/plain")
	if err := ipxeTemplate.Execute(w, map[string]string{
		"Kernel": s.KernelURL, "Initrd": s.InitrdURL, "Args": strings.Join(args, " "),
	}); err != nil {
		logger.Error(err, "writing iPXE script")
		return
	}
	logger.Info("served iPXE script", "wipe", firstBoot && len(s.WipeArgs) > 0)
}

func (s *Server) serveConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx).WithValues("host", r.PathValue("host"), "remote", r.RemoteAddr)
	h, err := s.authorize(r)
	if err != nil {
		logger.Info("refusing config request", "reason", err.Error())
		http.NotFound(w, r)
		return
	}
	config, err := s.renderConfig(ctx, h)
	if err != nil {
		logger.Error(err, "rendering machine config")
		http.Error(w, "config not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.updateStatus(ctx, h, func(p *infrav1.ProvisioningStatus, now metav1.Time) { p.ConfigServedAt = &now }); err != nil {
		logger.Error(err, "recording config fetch")
		http.Error(w, "try again", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(config)
	logger.Info("served machine config")
}

// renderConfig loads the bootstrap data of the claiming machine and adapts
// it to the host.
func (s *Server) renderConfig(ctx context.Context, h *infrav1.BootBoxHost) ([]byte, error) {
	ref := h.Status.ConsumerRef
	if ref == nil {
		return nil, errors.New("host has no consumer")
	}
	bbm := &infrav1.BootBoxMachine{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, bbm); err != nil {
		return nil, err
	}
	if bbm.UID != ref.UID {
		return nil, errors.New("consumer was replaced")
	}
	machine, err := util.GetOwnerMachine(ctx, s.Client, bbm.ObjectMeta)
	if err != nil {
		return nil, err
	}
	if machine == nil || machine.Spec.Bootstrap.DataSecretName == nil {
		return nil, errors.New("bootstrap data not ready")
	}
	secret := &corev1.Secret{}
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: machine.Namespace, Name: *machine.Spec.Bootstrap.DataSecretName}, secret); err != nil {
		return nil, err
	}
	data, ok := secret.Data["value"]
	if !ok {
		return nil, fmt.Errorf("bootstrap secret %s has no value", secret.Name)
	}
	hw, err := hardware.Get(ctx, s.Client, h.Namespace, h.Spec.HardwareName)
	if err != nil {
		return nil, err
	}
	res, err := hardware.ReservationOf(hw)
	if err != nil {
		return nil, err
	}
	return talosconfig.Apply(data, provision.Settings(h, res))
}

func (s *Server) updateStatus(ctx context.Context, h *infrav1.BootBoxHost, mutate func(*infrav1.ProvisioningStatus, metav1.Time)) error {
	now := metav1.NewTime(s.now())
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur := &infrav1.BootBoxHost{}
		if err := s.Client.Get(ctx, client.ObjectKeyFromObject(h), cur); err != nil {
			return err
		}
		mutate(&cur.Status.Provisioning, now)
		return s.Client.Status().Update(ctx, cur)
	})
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
