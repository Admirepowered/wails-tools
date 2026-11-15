//go:build !windows

package sniproxy

import "fmt"

func execCertutil(certPath string) ([]string, error) {
	return nil, fmt.Errorf("请在系统证书管理里手动导入：%s", certPath)
}
