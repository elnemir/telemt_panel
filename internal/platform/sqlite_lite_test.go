//go:build lite

package platform

import (
	"errors"
	"testing"
)

func TestLiteRejectsTransactionalPlatform(t *testing.T) {
	if Available {
		t.Fatal("lite capability incorrectly enabled")
	}
	if _, e := Open("ignored", nil); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}
