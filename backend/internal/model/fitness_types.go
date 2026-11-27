package model

import "strings"

// FlagSyncFitnessTypes controls access to the additional Fitness sample types.
const FlagSyncFitnessTypes = "sync.fitness_types"

// Space-separated policy versions whose text names goal, sex, weight and the new
// HealthKit types. Add a version here alongside its published policy text.
const profileBodyPolicyVersions = ""

// NamesProfileBody reports whether the policy version covers the profile body data.
func NamesProfileBody(version string) bool {
	for named := range strings.FieldsSeq(profileBodyPolicyVersions) {
		if version == named {
			return true
		}
	}
	return false
}
