package core

import (
	"net/url"
	"strconv"
	"strings"
)

// Declarative bind tag / label prefixes (Ginkgo labels, Godog Scenario tags).
const (
	BindIDPrefix   = "tracera.id:"
	BindNamePrefix = "tracera.name:"
)

// ParseBindTags reads `tracera.id:‹id›` and optional `tracera.name:‹name›`
// from runner labels or tags. The name may be URL-encoded so it can carry
// spaces inside a Gherkin tag. Returns id 0 when the suite did not bind.
func ParseBindTags(tags []string) (id int, name string) {
	for _, raw := range tags {
		tag := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "@"))
		switch {
		case strings.HasPrefix(tag, BindIDPrefix):
			if n, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(tag, BindIDPrefix))); err == nil && n > 0 {
				id = n
			}
		case strings.HasPrefix(tag, BindNamePrefix):
			value := strings.TrimSpace(strings.TrimPrefix(tag, BindNamePrefix))
			if decoded, err := url.QueryUnescape(value); err == nil {
				value = decoded
			}
			if value != "" {
				name = value
			}
		}
	}
	return id, name
}
