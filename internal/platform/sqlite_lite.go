//go:build lite

package platform

const Available = false

func Open(string, []byte) (*Store, error) { return nil, ErrUnavailable }
