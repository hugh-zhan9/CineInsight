//go:build linux

package services

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

func consolidationMachineID() (string, error) {
	raw, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	decoded, err := hex.DecodeString(id)
	if err != nil || len(decoded) != 16 || strings.Trim(id, "0") == "" {
		return "", fmt.Errorf("本机 machine-id 无效")
	}
	return id, nil
}
