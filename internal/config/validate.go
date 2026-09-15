package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// Validate checks the configuration and returns every problem it finds, joined
// into a single error.
//
// All problems are reported at once rather than one per run: an operator
// iterating on a node's configuration through a reboot cycle should not have to
// discover their mistakes one boot at a time.
//
// Validation performs no network access and no filesystem access, so it is safe
// to run in CI against a configuration for a node that does not exist yet.
func (c *Config) Validate() error {
	var problems []error

	problems = append(problems, c.validateRole()...)
	problems = append(problems, c.validateNetwork()...)
	problems = append(problems, c.validateStorage()...)
	problems = append(problems, c.validateJoin()...)
	problems = append(problems, c.validateNode()...)
	problems = append(problems, c.validateAddons()...)

	return errors.Join(problems...)
}

func (c *Config) validateRole() []error {
	switch c.Role {
	case RoleSingle, RoleController, RoleControllerWorker, RoleWorker:
		return nil
	case "":
		return []error{errors.New("role: required (single, controller, controller+worker or worker)")}
	default:
		return []error{fmt.Errorf("role: unknown value %q", c.Role)}
	}
}

func (c *Config) validateNetwork() []error {
	var problems []error

	for _, cidr := range []struct{ field, value string }{
		{"network.podCIDR", c.Network.PodCIDR},
		{"network.serviceCIDR", c.Network.ServiceCIDR},
	} {
		if cidr.value == "" {
			continue
		}

		if _, err := netip.ParsePrefix(cidr.value); err != nil {
			problems = append(problems,
				fmt.Errorf("%s: %q is not a valid CIDR", cidr.field, cidr.value))
		}
	}

	// Overlapping pod and service ranges produce a cluster that comes up and
	// then misroutes traffic in ways that are miserable to diagnose. Catching
	// it here costs nothing.
	pod, podErr := netip.ParsePrefix(c.Network.PodCIDR)
	svc, svcErr := netip.ParsePrefix(c.Network.ServiceCIDR)

	if podErr == nil && svcErr == nil {
		if pod.Overlaps(svc) {
			problems = append(problems, fmt.Errorf(
				"network: podCIDR %s overlaps serviceCIDR %s", pod, svc))
		}
	}

	switch c.Network.CNI {
	case CNIKubeRouter, CNICalico, CNICustom, "":
	default:
		problems = append(problems,
			fmt.Errorf("network.cni: unknown value %q", c.Network.CNI))
	}

	return problems
}

func (c *Config) validateStorage() []error {
	var problems []error

	switch c.Storage.Type {
	case StorageEtcd, StorageSQLite, "":
	default:
		problems = append(problems,
			fmt.Errorf("storage.type: unknown value %q", c.Storage.Type))
	}

	// SQLite cannot be shared between controllers. Accepting this combination
	// would produce a cluster that works until the second controller joins and
	// then fails in a way that looks like a networking problem.
	if c.Storage.Type == StorageSQLite && c.Role == RoleController {
		problems = append(problems, errors.New(
			"storage.type: sqlite cannot back a multi-controller cluster; use etcd, or role: single"))
	}

	return problems
}

func (c *Config) validateJoin() []error {
	var problems []error

	hasToken := c.Join.Token != ""
	hasSource := c.Join.TokenFrom != nil

	if hasToken && hasSource {
		problems = append(problems, errors.New(
			"join: set either token or tokenFrom, not both"))
	}

	// A worker with no way to authenticate cannot join anything. Failing here
	// is far kinder than a node that boots, looks healthy, and never appears in
	// the cluster.
	if c.Role == RoleWorker && !hasToken && !hasSource {
		problems = append(problems, errors.New(
			"join: required for role worker (set join.token or join.tokenFrom)"))
	}

	if c.Role == RoleSingle && (hasToken || hasSource) {
		problems = append(problems, errors.New(
			"join: meaningless for role single, which bootstraps its own cluster"))
	}

	if hasSource {
		problems = append(problems, c.Join.TokenFrom.validate()...)
	}

	return problems
}

func (t *TokenSource) validate() []error {
	var problems []error

	hasURL := t.URL != ""
	hasFile := t.File != ""

	switch {
	case hasURL && hasFile:
		problems = append(problems, errors.New(
			"join.tokenFrom: set either url or file, not both"))
	case !hasURL && !hasFile:
		problems = append(problems, errors.New(
			"join.tokenFrom: set either url or file"))
	}

	if hasURL {
		parsed, err := url.Parse(t.URL)
		switch {
		case err != nil:
			problems = append(problems,
				fmt.Errorf("join.tokenFrom.url: %q is not a valid URL", t.URL))
		case parsed.Scheme != "https":
			// A join token fetched over plain HTTP is a join token handed to
			// anyone on the path. There is no opt-out for this.
			problems = append(problems, fmt.Errorf(
				"join.tokenFrom.url: scheme must be https, got %q", parsed.Scheme))
		}
	}

	if hasFile && !strings.HasPrefix(t.File, "/") {
		problems = append(problems, fmt.Errorf(
			"join.tokenFrom.file: must be an absolute path, got %q", t.File))
	}

	return problems
}

func (c *Config) validateNode() []error {
	var problems []error

	for i, taint := range c.Node.Taints {
		if taint.Key == "" {
			problems = append(problems,
				fmt.Errorf("node.taints[%d].key: required", i))
		}

		switch taint.Effect {
		case "NoSchedule", "PreferNoSchedule", "NoExecute":
		case "":
			problems = append(problems,
				fmt.Errorf("node.taints[%d].effect: required", i))
		default:
			problems = append(problems, fmt.Errorf(
				"node.taints[%d].effect: must be NoSchedule, PreferNoSchedule or NoExecute, got %q",
				i, taint.Effect))
		}
	}

	return problems
}

func (c *Config) validateAddons() []error {
	var problems []error

	// Add-ons are installed by the control plane. A worker declaring them is
	// expressing an intent that will never be carried out.
	if len(c.Addons) > 0 && !c.Role.IsController() {
		problems = append(problems, fmt.Errorf(
			"addons: only a controller installs add-ons, but role is %q", c.Role))
	}

	repositories := make(map[string]bool)

	for _, addon := range c.Addons {
		if addon.Repository != nil {
			repositories[addon.Repository.Name] = true
		}
	}

	for i, addon := range c.Addons {
		if addon.Name == "" {
			problems = append(problems, fmt.Errorf("addons[%d].name: required", i))
		}

		if addon.Chart == "" {
			problems = append(problems, fmt.Errorf("addons[%d].chart: required", i))
			continue
		}

		repo, _, qualified := strings.Cut(addon.Chart, "/")
		if !qualified {
			problems = append(problems, fmt.Errorf(
				"addons[%d].chart: expected repository/chart, got %q", i, addon.Chart))
			continue
		}

		if !repositories[repo] {
			problems = append(problems, fmt.Errorf(
				"addons[%d].chart: repository %q is not declared by any addon", i, repo))
		}
	}

	return problems
}
