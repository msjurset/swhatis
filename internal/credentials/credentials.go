package credentials

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	serviceName      = "swhatis"
	DefaultGeminiRef = "op://Private/Gemini API Key/credential"
	DefaultKeyName   = "gemini-paid-api-key"
	OpRefKeyName     = "gemini-op-ref"
)

// Store saves a secret to the platform keychain under the given key.
func Store(key, value string) error {
	return platformStore(serviceName, key, value)
}

// Load retrieves a secret from the platform keychain by key.
// Returns empty string and nil error if the key doesn't exist.
func Load(key string) (string, error) {
	return platformLoad(serviceName, key)
}

// LoadFromService retrieves a secret from a specific keychain service name.
func LoadFromService(service, key string) (string, error) {
	return platformLoad(service, key)
}

// Delete removes a secret from the platform keychain by key.
func Delete(key string) error {
	return platformDelete(serviceName, key)
}

// ResolveOp calls `op read` to resolve a 1Password secret reference,
// triggering the 1Password biometric prompt if not unlocked.
func ResolveOp(ref string) (string, error) {
	ref = CleanOpRef(ref)
	cmd := exec.Command("op", "read", ref)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("op read %s: %s", ref, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("op read %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveAndCache resolves an op:// reference via 1Password CLI (triggering biometric check),
// stores both the resolved key and the op:// reference in the platform keychain under swhatis,
// and returns the value.
func ResolveAndCache(key, opRef string) (string, error) {
	val, err := ResolveOp(opRef)
	if err != nil {
		return "", err
	}
	if err := Store(key, val); err != nil {
		return "", fmt.Errorf("storing in keychain: %w", err)
	}
	_ = Store(OpRefKeyName, CleanOpRef(opRef))
	return val, nil
}

// ResolveGeminiKey resolves the Gemini API key in a portable, general order:
// 1. If forceAuth is true:
//    - If rawOrOpRef starts with "op://", calls 1Password CLI (`op read`) for biometric verification and caches the result in Keychain.
//    - If rawOrOpRef is a raw API key, stores it directly in the platform Keychain.
//    - If rawOrOpRef is empty, checks GEMINI_OP_REF / GEMINI_PAID_API_KEY / GEMINI_API_KEY or previously saved OpRefKeyName, falling back to DefaultGeminiRef.
// 2. Checks swhatis platform Keychain cache ("gemini-paid-api-key").
// 3. Checks GEMINI_PAID_API_KEY or GEMINI_API_KEY env var (resolving op:// via 1Password if set to a 1Password reference).
// 4. If `op` CLI is installed and either `-op-ref`, `GEMINI_OP_REF`, or a saved/default 1Password reference is available, resolves via `op read` and caches in Keychain.
func ResolveGeminiKey(opRef string, forceAuth bool) (string, error) {
	if envRef := strings.TrimSpace(os.Getenv("GEMINI_OP_REF")); envRef != "" && opRef == "" {
		opRef = envRef
	}

	if forceAuth {
		target := strings.TrimSpace(opRef)
		if target == "" {
			for _, envName := range []string{"GEMINI_PAID_API_KEY", "GEMINI_API_KEY"} {
				if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
					target = v
					break
				}
			}
		}
		if target == "" {
			if savedRef, err := Load(OpRefKeyName); err == nil && savedRef != "" {
				target = savedRef
			} else {
				target = DefaultGeminiRef
			}
		}
		if IsOpRef(target) {
			return ResolveAndCache(DefaultKeyName, target)
		}
		if err := Store(DefaultKeyName, target); err != nil {
			return "", fmt.Errorf("storing API key in keychain: %w", err)
		}
		return target, nil
	}

	// Normal lookup: 1. Keychain cache
	if val, err := Load(DefaultKeyName); err == nil && val != "" && !IsOpRef(val) {
		return val, nil
	}

	// 2. Environment variables (raw key or op:// reference)
	for _, envName := range []string{"GEMINI_PAID_API_KEY", "GEMINI_API_KEY"} {
		if envVal := strings.TrimSpace(os.Getenv(envName)); envVal != "" {
			if IsOpRef(envVal) {
				return ResolveAndCache(DefaultKeyName, envVal)
			}
			return envVal, nil
		}
	}

	// 3. 1Password CLI resolution (if op is available on PATH)
	if _, err := exec.LookPath("op"); err == nil {
		ref := strings.TrimSpace(opRef)
		if ref == "" {
			if savedRef, lErr := Load(OpRefKeyName); lErr == nil && savedRef != "" {
				ref = savedRef
			} else {
				ref = DefaultGeminiRef
			}
		}
		if IsOpRef(ref) {
			return ResolveAndCache(DefaultKeyName, ref)
		}
	}

	return "", fmt.Errorf("no Gemini API key found (set GEMINI_API_KEY or run `swhatis -auth`)")
}

// CleanOpRef removes surrounding quotes and whitespace from an op:// reference.
func CleanOpRef(val string) string {
	return strings.Trim(val, "\"' \t")
}

// IsOpRef returns true if the value is a 1Password reference (op:// prefix).
func IsOpRef(val string) bool {
	return strings.HasPrefix(CleanOpRef(val), "op://")
}
