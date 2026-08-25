package selfupdate

import (
	"strconv"
	"strings"
)

type semanticVersion struct {
	core       [3]uint64
	prerelease []string
}

func isNewer(current, latest string) bool {
	if normalizeVersion(current) == normalizeVersion(latest) {
		return false
	}
	currentVersion, currentOK := parseVersion(current)
	latestVersion, latestOK := parseVersion(latest)
	if !latestOK {
		return false
	}
	if !currentOK {
		return true
	}
	return compareVersions(currentVersion, latestVersion) < 0
}

func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func parseVersion(value string) (semanticVersion, bool) {
	value = normalizeVersion(value)
	value, _, _ = strings.Cut(value, "+")
	core, prerelease, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	var parsed semanticVersion
	for index, part := range parts {
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return semanticVersion{}, false
		}
		parsed.core[index] = number
	}
	if hasPrerelease {
		if prerelease == "" {
			return semanticVersion{}, false
		}
		parsed.prerelease = strings.Split(prerelease, ".")
	}
	return parsed, true
}

func compareVersions(left, right semanticVersion) int {
	for index := range left.core {
		if left.core[index] < right.core[index] {
			return -1
		}
		if left.core[index] > right.core[index] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		comparison := compareIdentifier(left.prerelease[index], right.prerelease[index])
		if comparison != 0 {
			return comparison
		}
	}
	switch {
	case len(left.prerelease) < len(right.prerelease):
		return -1
	case len(left.prerelease) > len(right.prerelease):
		return 1
	default:
		return 0
	}
}

func compareIdentifier(left, right string) int {
	leftNumber, leftNumeric := numericIdentifier(left)
	rightNumber, rightNumeric := numericIdentifier(right)
	switch {
	case leftNumeric && rightNumeric && leftNumber < rightNumber:
		return -1
	case leftNumeric && rightNumeric && leftNumber > rightNumber:
		return 1
	case leftNumeric && !rightNumeric:
		return -1
	case !leftNumeric && rightNumeric:
		return 1
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func numericIdentifier(value string) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return number, err == nil
}
