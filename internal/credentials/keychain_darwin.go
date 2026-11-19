//go:build darwin

package credentials

import (
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

func platformStore(service, key, value string) error {
	_ = platformDelete(service, key)

	cmd := exec.Command("security", "add-generic-password",
		"-s", service,
		"-a", key,
		"-w", value,
		"-U",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain store: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func platformLoad(service, key string) (string, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", service,
		"-a", key,
		"-w",
	)
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}

	result := strings.TrimSpace(string(out))

	if isHexEncoded(result) {
		decoded, err := hex.DecodeString(result)
		if err != nil {
			return "", fmt.Errorf("decoding hex keychain value: %w", err)
		}
		return string(decoded), nil
	}

	return result, nil
}

func isHexEncoded(s string) bool {
	if len(s) == 0 || len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 64
}

func platformDelete(service, key string) error {
	cmd := exec.Command("security", "delete-generic-password",
		"-s", service,
		"-a", key,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
