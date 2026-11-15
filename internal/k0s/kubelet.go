package k0s

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Kubelet is one Node object as the API server reports it.
type Kubelet struct {
	Name string `json:"name"`

	// Version is the kubelet's own report, such as v1.36.5+k0s. It carries no
	// k0s build, so only its minor is comparable with a release tag.
	Version string `json:"version"`
}

// kubeletPattern reads the Kubernetes minor out of a kubelet version.
var kubeletPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.`)

func (k Kubelet) minor() (int, int, error) {
	match := kubeletPattern.FindStringSubmatch(k.Version)
	if match == nil {
		return 0, 0, fmt.Errorf("%s reports kubelet %q: %w", k.Name, k.Version, ErrMalformedVersion)
	}

	major, errMajor := strconv.Atoi(match[1])
	minor, errMinor := strconv.Atoi(match[2])

	if errMajor != nil || errMinor != nil {
		return 0, 0, fmt.Errorf("%s reports kubelet %q: %w", k.Name, k.Version, ErrMalformedVersion)
	}

	return major, minor, nil
}

// CheckKubelets reports the kubelets still below the minor of to, and refuses
// to when it would leave one of them outside k0s's skew rules: more than one
// minor behind its controllers, or ahead of them.
//
// A kubelet one minor behind is legal, and a rollout stopped half-way leaves
// exactly that, so it is returned rather than refused.
func CheckKubelets(to Version, kubelets []Kubelet) ([]Kubelet, error) {
	var lagging, stranded []Kubelet

	for _, kubelet := range kubelets {
		major, minor, err := kubelet.minor()
		if err != nil {
			return nil, err
		}

		switch {
		case major != to.Major || minor > to.Minor || to.Minor-minor > 1:
			stranded = append(stranded, kubelet)
		case minor < to.Minor:
			lagging = append(lagging, kubelet)
		}
	}

	sortKubelets(lagging)
	sortKubelets(stranded)

	if len(stranded) > 0 {
		names := make([]string, 0, len(stranded))
		for _, kubelet := range stranded {
			names = append(names, fmt.Sprintf("%s (%s)", kubelet.Name, kubelet.Version))
		}

		return lagging, fmt.Errorf(
			"%w: moving a controller to %s would leave %s outside k0s's skew rules; "+
				"a worker may be one minor behind its controllers and never ahead, "+
				"so bring them within one minor of %s first",
			ErrSkew, to.MinorLine(), strings.Join(names, ", "), to.MinorLine())
	}

	return lagging, nil
}

func sortKubelets(kubelets []Kubelet) {
	sort.Slice(kubelets, func(i, j int) bool { return kubelets[i].Name < kubelets[j].Name })
}
