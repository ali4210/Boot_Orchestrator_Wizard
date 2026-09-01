package discovery

import "strings"

func toLower(s string) string { return strings.ToLower(s) }

func containsFold(s, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(s), lowerQuery)
}
