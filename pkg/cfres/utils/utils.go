// © 2025 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package utils

import "fmt"

// GetStringProperty safely extracts a string value from a properties map
func GetStringProperty(properties map[string]interface{}, key string) (string, error) {
	val, ok := properties[key]
	if !ok {
		return "", fmt.Errorf("required property %s not found", key)
	}
	strVal, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("property %s is not a string", key)
	}
	return strVal, nil
}

// StripEmptyCollections recursively removes empty arrays and maps from a map.
// The 0.83.0 PKL schema renders unset nullable Listing/Mapping fields as []/{}
// which cloud provider APIs may reject.
func StripEmptyCollections(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case []any:
			if len(val) == 0 {
				delete(m, k)
			} else {
				for _, elem := range val {
					if elemMap, ok := elem.(map[string]any); ok {
						StripEmptyCollections(elemMap)
					}
				}
			}
		case map[string]any:
			if len(val) == 0 {
				delete(m, k)
			} else {
				StripEmptyCollections(val)
			}
		}
	}
}

// GetInt64Property safely extracts an int64 value from a properties map with a default value
func GetInt64Property(properties map[string]interface{}, key string, defaultValue int64) int64 {
	if val, ok := properties[key]; ok {
		if numVal, ok := val.(float64); ok {
			return int64(numVal)
		}
	}
	return defaultValue
}
