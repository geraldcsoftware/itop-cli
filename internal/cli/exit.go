package cli

import (
	"errors"

	"github.com/geraldcsoftware/itop-cli/internal/itop"
)

// Process exit codes. A caller branches on these, so the meanings are fixed.
const (
	ExitOK          = 0
	ExitUsage       = 1 // config, usage or credential error, including iTop rejecting the login
	ExitUnreachable = 2 // iTop unreachable or timed out
	ExitSchema      = 3 // unknown class or field
	ExitAPI         = 4 // any other iTop error
	ExitRefused     = 5 // refused by the CLI's own write guard
	ExitNotFound    = 6 // ref or id not found
)

// exitFor maps an error to its exit code. Anything that is not an iTop
// failure, refusal or miss is a config, usage or credential error.
func exitFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var refused *itop.RefusedError
	var notFound *itop.NotFoundError
	var ie *itop.Error
	switch {
	case errors.As(err, &refused):
		return ExitRefused
	case errors.As(err, &notFound):
		return ExitNotFound
	case errors.As(err, &ie):
		switch ie.Kind {
		case itop.ErrUnreachable:
			return ExitUnreachable
		case itop.ErrSchema:
			return ExitSchema
		case itop.ErrAPI:
			return ExitAPI
		}
	}
	return ExitUsage
}
