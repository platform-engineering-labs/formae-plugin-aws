// © 2026 Platform Engineering Labs Inc.
//
// SPDX-License-Identifier: FSL-1.1-ALv2

package ccx

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"sort"
)

const iamRoleResourceType = "AWS::IAM::Role"

var (
	iamStatementStringFields = [...]string{"Action", "NotAction", "Resource", "NotResource"}
	iamPrincipalFields       = [...]string{"Principal", "NotPrincipal"}
	iamPrincipalStringKeys   = [...]string{"AWS", "Federated", "Service", "CanonicalUser"}
)

// stabilizeIAMRoleTrustPolicy preserves the caller's existing representation
// of IAM's documented unordered collections, but only after proving that the
// complete prior and actual policies are otherwise identical. It always starts
// from actual provider values; PriorProperties supplies ordering only.
func stabilizeIAMRoleTrustPolicy(properties map[string]any, actualProperties, priorProperties json.RawMessage) {
	actual, ok := decodeJSONObjectUseNumber(actualProperties)
	if !ok {
		return
	}
	actualPolicy, ok := actual["AssumeRolePolicyDocument"].(map[string]any)
	if !ok {
		return
	}
	// Keep the provider's policy numbers exact even when stabilization is not
	// possible. The generic properties decode uses float64, which cannot
	// distinguish adjacent large IAM Condition integers.
	properties["AssumeRolePolicyDocument"] = actualPolicy

	if len(priorProperties) == 0 {
		return
	}

	prior, ok := decodeJSONObjectUseNumber(priorProperties)
	if !ok {
		return
	}
	priorPolicy, ok := prior["AssumeRolePolicyDocument"].(map[string]any)
	if !ok {
		return
	}

	canonicalActual, actualOK := canonicalIAMPolicy(actualPolicy)
	canonicalPrior, priorOK := canonicalIAMPolicy(priorPolicy)
	if !actualOK || !priorOK || !reflect.DeepEqual(canonicalActual, canonicalPrior) {
		return
	}

	reordered, ok := reorderIAMPolicyLikePrior(actualPolicy, priorPolicy)
	if ok {
		properties["AssumeRolePolicyDocument"] = reordered
	}
}

// canonicalIAMPolicy returns a copy used only for equivalence checking. It
// sorts precisely the collections IAM documents as unordered and rejects
// malformed recognized shapes. Condition and unknown subtrees stay untouched,
// so their array order remains significant.
func canonicalIAMPolicy(policy map[string]any) (map[string]any, bool) {
	canonical := cloneJSONMap(policy)
	statement, ok := canonical["Statement"]
	if !ok {
		return nil, false
	}

	switch statement := statement.(type) {
	case map[string]any:
		if !canonicalizeIAMStatement(statement) {
			return nil, false
		}
	case []any:
		type keyedStatement struct {
			key       string
			statement map[string]any
		}
		statements := make([]keyedStatement, len(statement))
		for i, value := range statement {
			statementMap, ok := value.(map[string]any)
			if !ok || !canonicalizeIAMStatement(statementMap) {
				return nil, false
			}
			key, err := json.Marshal(statementMap)
			if err != nil {
				return nil, false
			}
			statements[i] = keyedStatement{key: string(key), statement: statementMap}
		}
		sort.SliceStable(statements, func(i, j int) bool { return statements[i].key < statements[j].key })
		ordered := make([]any, len(statements))
		for i, keyed := range statements {
			ordered[i] = keyed.statement
		}
		canonical["Statement"] = ordered
	default:
		return nil, false
	}

	return canonical, true
}

func canonicalizeIAMStatement(statement map[string]any) bool {
	for _, field := range iamStatementStringFields {
		if !canonicalizeIAMStringChoice(statement, field) {
			return false
		}
	}
	for _, field := range iamPrincipalFields {
		value, exists := statement[field]
		if !exists {
			continue
		}
		switch principal := value.(type) {
		case string:
		case map[string]any:
			for _, key := range iamPrincipalStringKeys {
				if !canonicalizeIAMStringChoice(principal, key) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func canonicalizeIAMStringChoice(container map[string]any, key string) bool {
	value, exists := container[key]
	if !exists {
		return true
	}
	switch value := value.(type) {
	case string:
		return true
	case []any:
		strings := make([]string, len(value))
		for i, item := range value {
			stringValue, ok := item.(string)
			if !ok {
				return false
			}
			strings[i] = stringValue
		}
		sort.Strings(strings)
		ordered := make([]any, len(strings))
		for i, stringValue := range strings {
			ordered[i] = stringValue
		}
		container[key] = ordered
		return true
	default:
		return false
	}
}

// reorderIAMPolicyLikePrior reorders a clone of actual. Statement matching
// uses queues keyed by normalized statement JSON, so duplicate statements and
// their multiplicity are retained.
func reorderIAMPolicyLikePrior(actual, prior map[string]any) (map[string]any, bool) {
	reordered := cloneJSONMap(actual)
	actualStatement := reordered["Statement"]
	priorStatement := prior["Statement"]

	switch actualStatement := actualStatement.(type) {
	case map[string]any:
		priorStatement, ok := priorStatement.(map[string]any)
		if !ok || !reorderIAMStatementLikePrior(actualStatement, priorStatement) {
			return nil, false
		}
	case []any:
		priorStatements, ok := priorStatement.([]any)
		if !ok || len(actualStatement) != len(priorStatements) {
			return nil, false
		}

		buckets := make(map[string][]map[string]any, len(actualStatement))
		for _, value := range actualStatement {
			statement, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			key, ok := canonicalIAMStatementKey(statement)
			if !ok {
				return nil, false
			}
			buckets[key] = append(buckets[key], statement)
		}

		ordered := make([]any, 0, len(priorStatements))
		for _, value := range priorStatements {
			priorStatement, ok := value.(map[string]any)
			if !ok {
				return nil, false
			}
			key, ok := canonicalIAMStatementKey(priorStatement)
			if !ok || len(buckets[key]) == 0 {
				return nil, false
			}
			statement := buckets[key][0]
			buckets[key] = buckets[key][1:]
			if !reorderIAMStatementLikePrior(statement, priorStatement) {
				return nil, false
			}
			ordered = append(ordered, statement)
		}
		reordered["Statement"] = ordered
	default:
		return nil, false
	}

	return reordered, true
}

func canonicalIAMStatementKey(statement map[string]any) (string, bool) {
	canonical := cloneJSONMap(statement)
	if !canonicalizeIAMStatement(canonical) {
		return "", false
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func reorderIAMStatementLikePrior(actual, prior map[string]any) bool {
	for _, field := range iamStatementStringFields {
		if !reorderIAMStringChoiceLikePrior(actual, prior, field) {
			return false
		}
	}
	for _, field := range iamPrincipalFields {
		actualValue, actualExists := actual[field]
		priorValue, priorExists := prior[field]
		if actualExists != priorExists {
			return false
		}
		if !actualExists {
			continue
		}
		actualPrincipal, actualMap := actualValue.(map[string]any)
		priorPrincipal, priorMap := priorValue.(map[string]any)
		if actualMap != priorMap {
			return false
		}
		if !actualMap {
			continue
		}
		for _, key := range iamPrincipalStringKeys {
			if !reorderIAMStringChoiceLikePrior(actualPrincipal, priorPrincipal, key) {
				return false
			}
		}
	}
	return true
}

func reorderIAMStringChoiceLikePrior(actual, prior map[string]any, key string) bool {
	actualValue, actualExists := actual[key]
	priorValue, priorExists := prior[key]
	if actualExists != priorExists {
		return false
	}
	if !actualExists {
		return true
	}

	actualStrings, actualIsArray := actualValue.([]any)
	priorStrings, priorIsArray := priorValue.([]any)
	if actualIsArray != priorIsArray {
		return false
	}
	if !actualIsArray {
		return reflect.DeepEqual(actualValue, priorValue)
	}

	buckets := make(map[string][]any, len(actualStrings))
	for _, value := range actualStrings {
		stringValue, ok := value.(string)
		if !ok {
			return false
		}
		buckets[stringValue] = append(buckets[stringValue], value)
	}
	ordered := make([]any, 0, len(priorStrings))
	for _, value := range priorStrings {
		stringValue, ok := value.(string)
		if !ok || len(buckets[stringValue]) == 0 {
			return false
		}
		ordered = append(ordered, buckets[stringValue][0])
		buckets[stringValue] = buckets[stringValue][1:]
	}
	if len(ordered) != len(actualStrings) {
		return false
	}
	actual[key] = ordered
	return true
}

func cloneJSONMap(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, child := range value {
		cloned[key] = cloneJSONValue(child)
	}
	return cloned
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneJSONMap(value)
	case []any:
		cloned := make([]any, len(value))
		for i, child := range value {
			cloned[i] = cloneJSONValue(child)
		}
		return cloned
	default:
		return value
	}
}

func decodeJSONObjectUseNumber(document json.RawMessage) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return object, true
}
