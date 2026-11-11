// Package redfish wraps the few Redfish operations the provider needs:
// inspect a system, request a one-time PXE boot and switch power.
package redfish

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// Config describes how to reach one BMC.
type Config struct {
	Address  string
	Username string
	Password string
	// Fingerprint is the SHA-256 of the leaf certificate (hex, colons optional).
	Fingerprint        string
	InsecureSkipVerify bool
	SystemID           string
}

// Info is what the BMC reports about the system.
type Info struct {
	Manufacturer string
	Model        string
	SerialNumber string
	BIOSVersion  string
	PowerState   string
}

// BMC is the provider's view of a baseboard management controller.
type BMC interface {
	Inspect(ctx context.Context) (Info, error)
	// PXEBootOnce makes the next boot a UEFI network boot.
	PXEBootOnce(ctx context.Context) error
	// PowerCycle restarts a running system and powers on a stopped one.
	PowerCycle(ctx context.Context) error
	PowerOff(ctx context.Context) error
}

// Factory creates a BMC client. Controllers take a Factory so tests can
// substitute a fake.
type Factory func(cfg Config) (BMC, error)

// New is the Factory for real BMCs.
func New(cfg Config) (BMC, error) {
	if cfg.Address == "" {
		return nil, errors.New("BMC address is empty")
	}
	tlsConfig, err := tlsConfigFor(cfg)
	if err != nil {
		return nil, err
	}
	return &client{cfg: cfg, httpClient: &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 15 * time.Second},
	}}, nil
}

// tlsConfigFor pins the leaf certificate when a fingerprint is set. Chain
// verification is replaced by the pin, since BMC certificates are
// self-signed and rarely carry the IP as SAN.
func tlsConfigFor(cfg Config) (*tls.Config, error) {
	if cfg.Fingerprint == "" {
		return &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify, MinVersion: tls.VersionTLS12}, nil //nolint:gosec // opt-in by the host spec
	}
	want, err := ParseFingerprint(cfg.Fingerprint)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyConnection below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("BMC presented no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if hex.EncodeToString(got[:]) != want {
				return fmt.Errorf("BMC certificate fingerprint %s does not match the pinned one", FormatFingerprint(got[:]))
			}
			return nil
		},
	}, nil
}

// ParseFingerprint normalizes a SHA-256 fingerprint to lowercase hex.
func ParseFingerprint(s string) (string, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "sha256:"), "SHA256:"))
	s = strings.ReplaceAll(s, ":", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("invalid SHA-256 fingerprint %q", s)
	}
	return s, nil
}

// FormatFingerprint renders a digest as colon-separated uppercase hex.
func FormatFingerprint(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprintf("%02X", v)
	}
	return strings.Join(parts, ":")
}

type client struct {
	cfg        Config
	httpClient *http.Client
}

// withSystem opens a connection, runs fn on the selected system and closes
// the connection. Basic auth avoids leaking Redfish sessions, which BMCs
// limit to a handful.
func (c *client) withSystem(ctx context.Context, fn func(*schemas.ComputerSystem) error) error {
	api, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint:   c.cfg.Address,
		Username:   c.cfg.Username,
		Password:   c.cfg.Password,
		BasicAuth:  true,
		HTTPClient: c.httpClient,
	})
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", c.cfg.Address, err)
	}
	defer api.Logout()

	systems, err := api.Service.Systems()
	if err != nil {
		return fmt.Errorf("listing systems: %w", err)
	}
	for _, s := range systems {
		if c.cfg.SystemID == "" || s.ID == c.cfg.SystemID {
			return fn(s)
		}
	}
	if c.cfg.SystemID != "" {
		return fmt.Errorf("system %q not found", c.cfg.SystemID)
	}
	return errors.New("BMC reports no systems")
}

func (c *client) Inspect(ctx context.Context) (Info, error) {
	var info Info
	err := c.withSystem(ctx, func(s *schemas.ComputerSystem) error {
		info = Info{
			Manufacturer: s.Manufacturer,
			Model:        s.Model,
			SerialNumber: s.SerialNumber,
			BIOSVersion:  s.BiosVersion,
			PowerState:   string(s.PowerState),
		}
		return nil
	})
	return info, err
}

func (c *client) PXEBootOnce(ctx context.Context) error {
	return c.withSystem(ctx, func(s *schemas.ComputerSystem) error {
		boot := &schemas.Boot{
			BootSourceOverrideTarget:  schemas.PxeBootSource,
			BootSourceOverrideEnabled: schemas.OnceBootSourceOverrideEnabled,
		}
		// Only send the mode when it changes: Dell iDRACs turn a mode write
		// into a BIOS configuration job with an extra reboot.
		if s.Boot.BootSourceOverrideMode != schemas.UEFIBootSourceOverrideMode {
			boot.BootSourceOverrideMode = schemas.UEFIBootSourceOverrideMode
		}
		return s.SetBoot(boot)
	})
}

func (c *client) PowerCycle(ctx context.Context) error {
	return c.withSystem(ctx, func(s *schemas.ComputerSystem) error {
		reset := schemas.ForceRestartResetType
		if s.PowerState == schemas.OffPowerState {
			reset = schemas.OnResetType
		}
		_, err := s.Reset(reset)
		return err
	})
}

func (c *client) PowerOff(ctx context.Context) error {
	return c.withSystem(ctx, func(s *schemas.ComputerSystem) error {
		if s.PowerState == schemas.OffPowerState {
			return nil
		}
		_, err := s.Reset(schemas.ForceOffResetType)
		return err
	})
}
