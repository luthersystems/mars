package vaulthelper

import (
	"fmt"
	"io"
)

func RequiredEnv(getenv func(string) string, names ...string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value := getenv(name)
		if value == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
		values[name] = value
	}
	return values, nil
}

func PrintSecret(stdout io.Writer, secret string) error {
	_, err := io.WriteString(stdout, secret)
	return err
}
