//go:build !darwin

package credentials

import (
	"fmt"
	"os/exec"
	"strings"
)

func platformStore(service, key, value string) error {
	cmd := exec.Command("secret-tool", "store", "--label="+service, "service", service, "account", key)
	cmd.Stdin = strings.NewReader(value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("secret-tool store: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func platformLoad(service, key string) (string, error) {
	cmd := exec.Command("secret-tool", "lookup", "service", service, "account", key)
	out, err := cmd.Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func platformDelete(service, key string) error {
	cmd := exec.Command("secret-tool", "clear", "service", service, "account", key)
	_ = cmd.Run()
	return nil
}
