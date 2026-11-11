package routes

import (
	"sort"

	"google.golang.org/protobuf/proto"

	panelv1 "github.com/aleksdotbar/vps/panel/gen/go/panel/v1"
)

// Replace prepares an environment-only update or recovery. It never restores a
// previous whole-server config over other environments' current contributions.
// Empty Domains removes that environment's routes. Inputs remain caller-owned;
// the result shares no mutable slices or domain messages with them.
func Replace(current []Contribution, replacement Contribution) ([]Contribution, error) {
	if _, err := checkContributions(current); err != nil {
		return nil, err
	}
	updated := make([]Contribution, 0, len(current)+1)
	for _, contribution := range current {
		if contribution.EnvironmentID != replacement.EnvironmentID {
			updated = append(updated, contribution)
		}
	}
	updated = append(updated, replacement)
	if _, err := checkContributions(updated); err != nil {
		return nil, err
	}
	for index := range updated {
		contribution := &updated[index]
		contribution.Services = append([]string(nil), contribution.Services...)
		domains := contribution.Domains
		contribution.Domains = make([]*panelv1.Domain, len(domains))
		for domainIndex, domain := range domains {
			contribution.Domains[domainIndex] = proto.Clone(domain).(*panelv1.Domain)
		}
	}
	sort.Slice(updated, func(i, j int) bool { return updated[i].EnvironmentID < updated[j].EnvironmentID })
	return updated, nil
}
