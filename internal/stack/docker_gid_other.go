//go:build !linux

package stack

import "os"

func dockerSocketGID(string) int { return max(0, os.Getgid()) }
