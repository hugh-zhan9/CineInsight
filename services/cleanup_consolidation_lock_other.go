//go:build !darwin && !linux

package services

import "fmt"

func consolidationMachineID() (string, error) {
	return "", fmt.Errorf("此平台不支持集中整理的稳定本机身份")
}
func acquireConsolidationLock(string, string) (func(), error) {
	return nil, fmt.Errorf("此平台不支持集中整理的进程锁")
}
