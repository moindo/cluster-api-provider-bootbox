// Package talosconfig adapts the machine config produced by the bootstrap
// provider (CABPT) to one host before it is served to the machine.
package talosconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"
)

// LegacyKind selects the v1alpha1 document, which has no "kind" field.
const LegacyKind = "v1alpha1"

// Patch is a JSON merge patch for the documents matching Kind and Name.
type Patch struct {
	Kind  string
	Name  string
	Merge []byte // JSON
}

// HostSettings are the per-host values injected into every config.
type HostSettings struct {
	Hostname   string
	ProviderID string
	Patches    []Patch
}

var separator = regexp.MustCompile(`(?m)^---[ \t]*$`)

// Apply sets hostname and kubelet providerID and applies the host patches.
// Every patch must match at least one document.
func Apply(config []byte, h HostSettings) ([]byte, error) {
	docs, err := split(config)
	if err != nil {
		return nil, err
	}

	legacy := -1
	hostnameDoc := -1
	for i, d := range docs {
		switch kindOf(d) {
		case LegacyKind:
			legacy = i
		case "HostnameConfig":
			hostnameDoc = i
		}
	}
	if legacy < 0 {
		return nil, fmt.Errorf("machine config has no v1alpha1 document")
	}

	machine := map[string]any{}
	if h.ProviderID != "" {
		machine["kubelet"] = map[string]any{"extraConfig": map[string]any{"providerID": h.ProviderID}}
	}
	if h.Hostname != "" {
		if hostnameDoc >= 0 {
			// Talos >= 1.12 documents: the hostname lives in HostnameConfig.
			if err := merge(docs[hostnameDoc], map[string]any{"hostname": h.Hostname, "auto": nil}); err != nil {
				return nil, err
			}
		} else {
			machine["network"] = map[string]any{"hostname": h.Hostname}
		}
	}
	if len(machine) > 0 {
		if err := merge(docs[legacy], map[string]any{"machine": machine}); err != nil {
			return nil, err
		}
	}

	for _, p := range h.Patches {
		var patch map[string]any
		if err := json.Unmarshal(p.Merge, &patch); err != nil {
			return nil, fmt.Errorf("patch for %s %q: %w", p.Kind, p.Name, err)
		}
		matched := false
		for _, d := range docs {
			if kindOf(d) == p.Kind && (p.Name == "" || d["name"] == p.Name) {
				if err := merge(d, patch); err != nil {
					return nil, err
				}
				matched = true
			}
		}
		if !matched {
			return nil, fmt.Errorf("patch for %s %q matches no document", p.Kind, p.Name)
		}
	}

	return join(docs)
}

func kindOf(d map[string]any) string {
	if k, ok := d["kind"].(string); ok {
		return k
	}
	if v, ok := d["version"].(string); ok && v == LegacyKind {
		return LegacyKind
	}
	return ""
}

// merge applies a JSON merge patch to d in place.
func merge(d map[string]any, patch map[string]any) error {
	orig, err := json.Marshal(d)
	if err != nil {
		return err
	}
	p, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	out, err := jsonpatch.MergePatch(orig, p)
	if err != nil {
		return err
	}
	for k := range d {
		delete(d, k)
	}
	return json.Unmarshal(out, &d)
}

func split(config []byte) ([]map[string]any, error) {
	var docs []map[string]any
	for i, part := range separator.Split(string(config), -1) {
		if len(bytes.TrimSpace([]byte(part))) == 0 {
			continue
		}
		d := map[string]any{}
		if err := yaml.Unmarshal([]byte(part), &d); err != nil {
			return nil, fmt.Errorf("document %d: %w", i, err)
		}
		if len(d) > 0 {
			docs = append(docs, d)
		}
	}
	return docs, nil
}

func join(docs []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	for i, d := range docs {
		if i > 0 {
			buf.WriteString("---\n")
		}
		b, err := yaml.Marshal(d)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	return buf.Bytes(), nil
}
