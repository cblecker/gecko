package hc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	privatev1 "github.com/openshift-online/gecko/platform-api/api/private/v1"
)

// Keep these expressions aligned with the GCPResourceTag schema markers,
// which mirror HyperShift. Format tests also check the generated API schema.
var (
	resourceTagKeyPattern          = regexp.MustCompile(`^[a-zA-Z0-9]([0-9A-Za-z_.-]{0,61}[a-zA-Z0-9])?$`)
	resourceTagValuePattern        = regexp.MustCompile(`^[a-zA-Z0-9]([0-9A-Za-z_.@%=+:,*#&()\[\]{}\- ]{0,61}[a-zA-Z0-9])?$`)
	resourceTagOrganizationPattern = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
	resourceTagParentPattern       = regexp.MustCompile(`(^[1-9][0-9]{0,31}$)|(^[a-z][a-z0-9-]{4,28}[a-z0-9]$)`)
)

// LoadResourceTags reads the JSON tag list mounted from the HC configuration.
// Empty lists and null disable tags. Invalid configuration fails startup.
// Errors omit the configured path and tag contents because they reach startup logs.
func LoadResourceTags(path string) ([]privatev1.GCPResourceTag, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// PathError includes the configured path in its message. Keep only the
		// underlying cause so errors.Is still identifies filesystem failures.
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return nil, fmt.Errorf("read resource tags file: %w", err)
	}
	var tags []privatev1.GCPResourceTag
	if err := json.Unmarshal(data, &tags); err != nil {
		// Decoder errors can contain input values. Report only their category
		// and offset rather than wrapping the original error into startup logs.
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			return nil, fmt.Errorf("parse resource tags file: invalid JSON at byte %d", syntaxErr.Offset)
		case errors.As(err, &typeErr):
			return nil, fmt.Errorf("parse resource tags file: unexpected value type at byte %d", typeErr.Offset)
		default:
			return nil, errors.New("parse resource tags file: invalid tag list")
		}
	}
	if len(tags) > 50 {
		return nil, fmt.Errorf("resource tags must contain at most 50 entries")
	}
	seen := make(map[string]bool, len(tags))
	for i, tag := range tags {
		if err := validateResourceTag(tag); err != nil {
			return nil, fmt.Errorf("resource tag at index %d: %w", i, err)
		}
		if seen[tag.Key] {
			return nil, fmt.Errorf("duplicate resource tag key at index %d", i)
		}
		seen[tag.Key] = true
	}
	return tags, nil
}

// mergeResourceTags gives platform-configured tags precedence over per-cluster
// tags by short key, including their parentID. A fresh sorted list avoids
// mutating either input and prevents spurious changes to an atomic tag list.
// The Firestore transport checks the combined count after selecting preserved
// tags, so an oversized desired union cannot block an existing valid desire.
func mergeResourceTags(requested, configured []privatev1.GCPResourceTag, projectID string) ([]privatev1.GCPResourceTag, error) {
	byKey := make(map[string]privatev1.GCPResourceTag, len(requested)+len(configured))
	for _, tag := range requested {
		byKey[tag.Key] = tag
	}
	for _, tag := range configured {
		byKey[tag.Key] = tag
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var tags []privatev1.GCPResourceTag
	for _, key := range keys {
		tag := byKey[key]
		if err := validateResourceTag(tag); err != nil {
			return nil, fmt.Errorf("invalid merged resource tag: %w", err)
		}
		if tag.ParentID != nil && !resourceTagOrganizationPattern.MatchString(*tag.ParentID) && *tag.ParentID != projectID {
			return nil, fmt.Errorf("resource tag parentID must be an organization ID or match the cluster GCP project ID")
		}
		tags = append(tags, *tag.DeepCopy())
	}
	return tags, nil
}

// validateResourceTag checks the syntax that can be validated without knowing
// the target project. Project-scoped parent matching is checked after merging.
func validateResourceTag(tag privatev1.GCPResourceTag) error {
	if tag.Key == "" || tag.Value == "" {
		return fmt.Errorf("resource tags require non-empty key and value")
	}
	if !resourceTagKeyPattern.MatchString(tag.Key) {
		return fmt.Errorf("resource tag key must be 1-63 characters, start and end with an ASCII letter or digit, and contain only ASCII letters, digits, '.', '_' or '-'")
	}
	if !resourceTagValuePattern.MatchString(tag.Value) {
		return fmt.Errorf("resource tag value must be 1-63 characters, start and end with an ASCII letter or digit, and contain only ASCII letters, digits, spaces or the characters _-.@%%=+:,*#&()[]{}")
	}
	if tag.ParentID != nil && !resourceTagParentPattern.MatchString(*tag.ParentID) {
		return fmt.Errorf("resource tag parentID must be a numeric organization ID of 1-32 digits without a leading zero, or a valid GCP project ID of 6-30 characters")
	}
	return nil
}
