package host

import (
	"context"
	"errors"
)

// Privileged installation uses Linux descriptor and ownership semantics.
// Development builds must fail closed rather than emulate those operations.
func LoadPrivilegedPolicy(string) (PrivilegedPolicy, error) {
	return PrivilegedPolicy{}, errors.New("privileged installation requires Linux")
}

func RunPrivilegedInstall(context.Context, string, string, string) error {
	return errors.New("privileged installation requires Linux")
}
