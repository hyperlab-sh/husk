package services

import "github.com/hyperlab-sh/husk/pkg/services/settings"

// newSet builds a Settings service whose schema must be valid.
func newSet(opts settings.Options) *settings.Service {
	s, err := settings.New(opts)
	if err != nil {
		panic(err)
	}
	return s
}

// hasProblem reports whether problems holds one with the given code and severity.
func hasProblem(problems []Problem, code, severity string) bool {
	for _, p := range problems {
		if p.Code == code && p.Severity == severity {
			return true
		}
	}
	return false
}
