// Package duplicate parses PostgreSQL unique violation errors (SQLSTATE 23505) of lib/pq.
//
// Most code should use sqlkit.IsDuplicate and sqlkit.DuplicateFields instead.
package duplicate

import (
	"regexp"
	"strings"

	"github.com/lib/pq"
)

var detailValueRe = regexp.MustCompile(`\(([^)]+)\)=\(([^)]+)\)`)

// Error describes a unique violation.
type Error struct {
	// Field is the column guessed from the constraint name ("users_email_key" -> "email").
	Field string
	// Value is the duplicated value taken from the error detail.
	Value string
	// Constraint is the name of the violated constraint.
	Constraint string
}

// Handle extracts the field, value and constraint of a unique violation from err.
func Handle(err *pq.Error) *Error {
	// Extract field name from constraint name
	// For constraint "partners_name_key", extract "name"
	field := extractFieldFromConstraint(err.Constraint)

	// Extract the duplicate value from the detail message
	value := extractValueFromDetail(err.Detail)

	return &Error{
		Field:      field,
		Value:      value,
		Constraint: err.Constraint,
	}
}

func extractFieldFromConstraint(constraint string) string {
	// Handle different constraint naming patterns
	if strings.Contains(constraint, "_key") {
		// Pattern: "table_field_key" -> extract "field"
		parts := strings.Split(constraint, "_")
		if len(parts) >= 2 {
			return parts[len(parts)-2] // Get the second-to-last part
		}
	}

	if strings.Contains(constraint, "_unique") {
		// Pattern: "table_field_unique" -> extract "field"
		parts := strings.Split(constraint, "_")
		if len(parts) >= 2 {
			return parts[len(parts)-2]
		}
	}

	return constraint
}

func extractValueFromDetail(detail string) string {
	// PostgreSQL detail format: "Key (field)=(value) already exists."
	matches := detailValueRe.FindStringSubmatch(detail)
	if len(matches) >= 3 {
		return matches[2] // Return the value part
	}
	return ""
}

// ParseDetail parses the Detail of a unique violation: "Key (a, b)=(x, y) already exists.".
//
// fields are the names inside the first parentheses split by ", "; value is the whole content of
// the second parentheses (values may contain commas). An index expression such as
// "lower(name::text)" is returned as a single field as is. ok is false if detail is not recognized.
func ParseDetail(detail string) (fields []string, value string, ok bool) {
	const (
		keyPrefix = "Key ("
		sep       = ")=("
	)

	start := strings.Index(detail, keyPrefix)
	if start < 0 {
		return nil, "", false
	}

	rest := detail[start+len(keyPrefix):]

	end := strings.Index(rest, sep)
	if end <= 0 {
		return nil, "", false
	}

	rawFields := rest[:end]
	rawValue := rest[end+len(sep):]

	closing := strings.LastIndex(rawValue, ")")
	if closing < 0 {
		return nil, "", false
	}

	return strings.Split(rawFields, ", "), rawValue[:closing], true
}
