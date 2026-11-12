package main

import (
	"encoding/base64"
	"errors"
	"os"
)

func nodeInstallKey(name string) ([]byte, error) {
	if name == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(os.Getenv(name))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, errors.New("node installation key must be 32 bytes encoded in base64")
	}
	return key, nil
}
