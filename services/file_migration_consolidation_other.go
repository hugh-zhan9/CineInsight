//go:build !darwin && !linux

package services

import "fmt"

func publishConsolidationNoReplace(string, string) error {
	return fmt.Errorf("此平台不支持排他发布")
}
