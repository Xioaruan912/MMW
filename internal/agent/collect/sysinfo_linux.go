//go:build linux

package collect

import (
	"os"
	"runtime"
	"strings"
	"syscall"
)

// SysInfo 是一台机器的静态画像。
type SysInfo struct {
	Hostname string
	OS       string
	Arch     string
	Kernel   string
	Distro   string
	BootID   string
}

// GatherSysInfo 采集静态系统信息（内核版本、发行版、boot id）。
func GatherSysInfo() SysInfo {
	host, _ := os.Hostname()
	info := SysInfo{
		Hostname: host,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Kernel:   kernelVersion(),
		Distro:   distroName(),
		BootID:   strings.TrimSpace(readFileTrim("/proc/sys/kernel/random/boot_id")),
	}
	return info
}

func kernelVersion() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	return charsToString(u.Release[:])
}

func charsToString(ca []int8) string {
	b := make([]byte, 0, len(ca))
	for _, c := range ca {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// distroName 从 /etc/os-release 取 PRETTY_NAME。
func distroName() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if key, val, ok := strings.Cut(line, "="); ok && key == "PRETTY_NAME" {
			return strings.Trim(val, `"'`)
		}
	}
	return ""
}

func readFileTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
