// Package k0s renders k0s configuration from a Corium configuration and drives
// the k0s binary.
package k0s

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/Corium-OS/Corium/internal/config"
)

// ConfigPath is where k0s reads its cluster configuration.
const ConfigPath = "/etc/k0s/k0s.yaml"

// Rendered configuration is built as a plain map rather than typed structs.
//
// Corium models only the fraction of k0s it exposes, and a typed
// representation would have to model all of it to round-trip an operator's
// patch without dropping the fields Corium does not know about. A map
// round-trips everything.
type object = map[string]any

// Render produces the k0s cluster configuration for a node.
//
// The operator's patch is applied last, so any value Corium computed can be
// overridden — including ones Corium considers load-bearing. That is the point
// of an escape hatch.
//
// The result must be byte-identical on every controller of an HA cluster: the
// VRRP password, router ID and virtual IP are a shared agreement, and two
// controllers rendering different files disagree about who owns the address.
func Render(cfg *config.Config) ([]byte, error) {
	doc := object{
		"apiVersion": "k0s.k0sproject.io/v1beta1",
		"kind":       "ClusterConfig",
		"metadata": object{
			"name": cfg.Cluster.Name,
		},
		"spec": renderSpec(cfg),
	}

	if len(cfg.K0s.Patch) > 0 {
		doc = merge(doc, cfg.K0s.Patch)
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshalling k0s config: %w", err)
	}

	return data, nil
}

func renderSpec(cfg *config.Config) object {
	spec := object{
		"network": renderNetwork(cfg),
		"storage": renderStorage(cfg),
		// Corium does not phone home, and neither should the clusters it
		// builds unless the operator asks for it.
		"telemetry": object{"enabled": false},
	}

	if api := renderAPI(cfg); len(api) > 0 {
		spec["api"] = api
	}

	if ext := renderExtensions(cfg); ext != nil {
		spec["extensions"] = ext
	}

	return spec
}

func renderAPI(cfg *config.Config) object {
	api := object{}

	if cfg.Cluster.Endpoint != "" {
		api["externalAddress"] = cfg.Cluster.Endpoint
	}

	// The endpoint must be in the certificate, or every client reaching the
	// cluster through it fails verification. Adding it implicitly removes a
	// foot-gun that otherwise only shows up after the cluster is running.
	sans := make([]string, 0, len(cfg.Cluster.SubjectAltNames)+1)
	seen := make(map[string]bool)

	for _, name := range append([]string{cfg.Cluster.Endpoint}, cfg.Cluster.SubjectAltNames...) {
		if name == "" || seen[name] {
			continue
		}

		seen[name] = true
		sans = append(sans, name)
	}

	if len(sans) > 0 {
		api["sans"] = sans
	}

	return api
}

func renderNetwork(cfg *config.Config) object {
	network := object{
		"podCIDR":     cfg.Network.PodCIDR,
		"serviceCIDR": cfg.Network.ServiceCIDR,
	}

	if cplb := renderControlPlaneLoadBalancing(cfg); cplb != nil {
		network["controlPlaneLoadBalancing"] = cplb
	}

	switch cfg.Network.CNI {
	case config.CNIKubeRouter:
		network["provider"] = "kuberouter"
	case config.CNICalico:
		network["provider"] = "calico"
	case config.CNICustom:
		// k0s deploys nothing and leaves the cluster without pod networking
		// until the operator installs a CNI. Nodes stay NotReady until then,
		// which is expected rather than broken.
		network["provider"] = "custom"
	}

	return network
}

func renderStorage(cfg *config.Config) object {
	if cfg.Storage.Type == config.StorageSQLite {
		// k0s reaches SQLite through kine; "sqlite" is Corium's name for it
		// because that is what the operator is actually choosing.
		return object{"type": "kine"}
	}

	return object{"type": "etcd"}
}

// renderExtensions maps add-ons onto the k0s Helm extension mechanism, which
// installs charts at bootstrap without a Helm binary or an in-cluster operator.
func renderExtensions(cfg *config.Config) object {
	if len(cfg.Addons) == 0 {
		return nil
	}

	var (
		repositories []object
		charts       []object
		declared     = make(map[string]bool)
	)

	for _, addon := range cfg.Addons {
		if addon.Repository != nil && !declared[addon.Repository.Name] {
			declared[addon.Repository.Name] = true
			repositories = append(repositories, object{
				"name": addon.Repository.Name,
				"url":  addon.Repository.URL,
			})
		}

		chart := object{
			"name":      addon.Name,
			"chartname": addon.Chart,
			"namespace": addon.Namespace,
		}

		if addon.Version != "" {
			chart["version"] = addon.Version
		}

		if len(addon.Values) > 0 {
			// k0s takes chart values as a YAML string, not as structured data.
			values, err := yaml.Marshal(addon.Values)
			if err == nil {
				chart["values"] = string(values)
			}
		}

		charts = append(charts, chart)
	}

	helm := object{"charts": charts}
	if len(repositories) > 0 {
		helm["repositories"] = repositories
	}

	return object{"helm": helm}
}

// renderControlPlaneLoadBalancing gives the control plane one address that
// outlives any single controller.
//
// The controllers run VRRP between themselves and one of them holds the virtual
// IP; when it fails, another takes over. This replaces the external load
// balancer an HA control plane would otherwise need — which matters because
// that load balancer would have to exist before the cluster it fronts.
func renderControlPlaneLoadBalancing(cfg *config.Config) object {
	if !cfg.HA.Enabled {
		return nil
	}

	instance := object{
		"virtualIPs": []string{cfg.HA.VirtualIP},
		"authPass":   cfg.HA.AuthPass,
	}

	if cfg.HA.Interface != "" {
		instance["interface"] = cfg.HA.Interface
	}

	if cfg.HA.VirtualRouterID != 0 {
		instance["virtualRouterID"] = cfg.HA.VirtualRouterID
	}

	if len(cfg.HA.UnicastPeers) > 0 {
		// Most clouds drop multicast, so VRRP has to be told explicitly who its
		// peers are.
		instance["unicastPeers"] = cfg.HA.UnicastPeers
	}

	return object{
		"enabled": true,
		"type":    "Keepalived",
		"keepalived": object{
			"vrrpInstances": []object{instance},
		},
	}
}

// merge recursively merges src into dst and returns the result.
//
// Maps are merged key by key; every other type, including slices, is replaced
// wholesale. Replacing slices rather than appending is the behaviour an
// operator writing a patch expects: a patch that sets a list means that list,
// not that list added to whatever was already there.
func merge(dst, src object) object {
	out := make(object, len(dst))
	for k, v := range dst {
		out[k] = v
	}

	for key, srcValue := range src {
		dstValue, present := out[key]
		if !present {
			out[key] = srcValue
			continue
		}

		dstMap, dstIsMap := toObject(dstValue)
		srcMap, srcIsMap := toObject(srcValue)

		if dstIsMap && srcIsMap {
			out[key] = merge(dstMap, srcMap)
			continue
		}

		out[key] = srcValue
	}

	return out
}

// toObject normalises the two map shapes YAML decoding can produce.
func toObject(v any) (object, bool) {
	switch typed := v.(type) {
	case object:
		return typed, true
	case map[any]any:
		converted := make(object, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, false
			}

			converted[stringKey] = value
		}

		return converted, true
	default:
		return nil, false
	}
}
