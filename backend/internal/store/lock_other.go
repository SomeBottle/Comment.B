//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package store

import (
	"errors"
	"os"
)

// lockWorkspace 在尚未实现持久化和进程锁的平台明确拒绝启动
func lockWorkspace(f *os.File) error {
	return errors.New("当前平台不支持工作区进程锁，请使用 Linux、macOS 或 BSD")
}
