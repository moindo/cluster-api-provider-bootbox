// Package provision holds what the controllers and the boot server share:
// naming of provider IDs and token secrets, boot URLs and the per-host config
// settings.
package provision

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1 "github.com/moindo/cluster-api-provider-bootbox/api/v1alpha1"
	"github.com/moindo/cluster-api-provider-bootbox/internal/hardware"
	"github.com/moindo/cluster-api-provider-bootbox/internal/talosconfig"
)

const (
	// ProviderIDPrefix is the scheme of provider IDs set by this provider.
	ProviderIDPrefix = "bootbox://"

	tokenKey = "token"

	// LabelHost marks token secrets with the name of their host.
	LabelHost = "infrastructure.cluster.x-k8s.io/bootbox-host"
)

// ProviderID returns bootbox://<namespace>/<name> for a host.
func ProviderID(h *infrav1.BootBoxHost) string {
	return ProviderIDPrefix + h.Namespace + "/" + h.Name
}

// Hostname returns the node hostname for a host.
func Hostname(h *infrav1.BootBoxHost, r hardware.Reservation) string {
	switch {
	case h.Spec.Hostname != "":
		return h.Spec.Hostname
	case r.Hostname != "":
		return r.Hostname
	default:
		return h.Name
	}
}

// Settings returns the per-host changes applied to the served config.
func Settings(h *infrav1.BootBoxHost, r hardware.Reservation) talosconfig.HostSettings {
	s := talosconfig.HostSettings{Hostname: Hostname(h, r), ProviderID: ProviderID(h)}
	for _, p := range h.Spec.ConfigPatches {
		s.Patches = append(s.Patches, talosconfig.Patch{Kind: p.Kind, Name: p.Name, Merge: p.Patch.Raw})
	}
	return s
}

// TokenSecretName is the Secret holding the provisioning token of a host.
func TokenSecretName(h *infrav1.BootBoxHost) string { return h.Name + "-boot-token" }

// EnsureToken returns the provisioning token of a host, creating it if needed.
func EnsureToken(ctx context.Context, c client.Client, h *infrav1.BootBoxHost) (string, error) {
	s := &corev1.Secret{}
	key := client.ObjectKey{Namespace: h.Namespace, Name: TokenSecretName(h)}
	err := c.Get(ctx, key, s)
	if err == nil {
		return string(s.Data[tokenKey]), nil
	}
	if !apierrors.IsNotFound(err) {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: h.Namespace, Name: key.Name,
			Labels: map[string]string{LabelHost: h.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(h,
				infrav1.GroupVersion.WithKind("BootBoxHost"))},
		},
		Data: map[string][]byte{tokenKey: []byte(token)},
	}
	if err := c.Create(ctx, s); err != nil {
		return "", err
	}
	return token, nil
}

// DeleteToken removes the provisioning token, which ends the config window.
func DeleteToken(ctx context.Context, c client.Client, h *infrav1.BootBoxHost) error {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: h.Namespace, Name: TokenSecretName(h)}}
	return client.IgnoreNotFound(c.Delete(ctx, s))
}

// CheckToken compares a presented token with the stored one in constant time.
func CheckToken(ctx context.Context, r client.Reader, h *infrav1.BootBoxHost, presented string) error {
	s := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: h.Namespace, Name: TokenSecretName(h)}, s); err != nil {
		return err
	}
	want := s.Data[tokenKey]
	if len(want) == 0 || subtle.ConstantTimeCompare(want, []byte(presented)) != 1 {
		return fmt.Errorf("token mismatch")
	}
	return nil
}

// BootURLs builds the per-host iPXE and config URLs below base.
type BootURLs struct{ Base *url.URL }

func (b BootURLs) path(h *infrav1.BootBoxHost, token, file string) string {
	u := *b.Base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/boot/" + url.PathEscape(h.Namespace) + "/" +
		url.PathEscape(h.Name) + "/" + token + "/" + file
	return u.String()
}

// IPXE is the URL smee hands to the host's iPXE.
func (b BootURLs) IPXE(h *infrav1.BootBoxHost, token string) string {
	return b.path(h, token, "boot.ipxe")
}

// Config is the URL Talos fetches its machine config from.
func (b BootURLs) Config(h *infrav1.BootBoxHost, token string) string {
	return b.path(h, token, "config")
}
