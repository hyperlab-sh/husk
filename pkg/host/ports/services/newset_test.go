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
