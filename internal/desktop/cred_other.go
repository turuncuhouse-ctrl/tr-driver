//go:build !windows

package desktop

import "os"

func WriteCredential(target, username, secret string) error {
	_ = target
	_ = username
	path := DefaultDataDir() + "/.device_token"
	return os.WriteFile(path, []byte(secret), 0o600)
}

func ReadCredential(target string) (string, error) {
	_ = target
	path := DefaultDataDir() + "/.device_token"
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

func DeleteCredential(target string) error {
	_ = target
	path := DefaultDataDir() + "/.device_token"
	_ = os.Remove(path)
	return nil
}
