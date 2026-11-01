//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"fmt"
	"os"
	"syscall"
)

// lockWorkspace 使用内核锁确保一个克隆只有一个写入进程，退出后自动释放
func lockWorkspace(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("数据仓库已被其他进程占用: %w", err)
	}
	return nil
}
