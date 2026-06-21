package realbackend

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// defaultBudgetMB is the conservative fallback the governor uses whenever
// host RAM can't be detected at all (unsupported OS, permission error,
// parse failure). Detection failing never blocks startup or refuses all
// real backends outright — it just runs with this small, safe default.
const defaultBudgetMB = 512

// budgetFraction is the conservative slice of detected host RAM the
// governor is allowed to commit to real backends in total, leaving
// headroom for the host OS and whatever else is running, per the
// roadmap's note on this.
const budgetFraction = 0.25

// DetectHostRAMMB returns the host's total physical RAM in MB, or
// (0, false) if it couldn't be determined. Implemented via small
// platform-specific shell-outs (reading /proc/meminfo on Linux,
// PowerShell's Get-CimInstance on Windows, sysctl on macOS) instead of a
// new Go module dependency — the same shell-out-rather-than-SDK approach
// DetectDocker uses.
func DetectHostRAMMB() (int, bool) {
	switch runtime.GOOS {
	case "linux":
		return detectHostRAMLinux()
	case "windows":
		return detectHostRAMWindows()
	case "darwin":
		return detectHostRAMDarwin()
	default:
		return 0, false
	}
}

// DetectBudgetMB returns the working RAM budget the governor should use:
// budgetFraction of detected host RAM (floored at defaultBudgetMB so a
// tiny/odd detection result never produces an unusably small budget), or
// defaultBudgetMB outright if detection fails. Always succeeds.
func DetectBudgetMB() int {
	totalMB, ok := DetectHostRAMMB()
	if !ok || totalMB <= 0 {
		return defaultBudgetMB
	}
	budget := int(float64(totalMB) * budgetFraction)
	if budget < defaultBudgetMB {
		return defaultBudgetMB
	}
	return budget
}

func detectHostRAMLinux() (int, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, false
		}
		return kb / 1024, true
	}
	return 0, false
}

func detectHostRAMWindows() (int, bool) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory").Output()
	if err != nil {
		return 0, false
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, false
	}
	return int(bytes / (1024 * 1024)), true
}

func detectHostRAMDarwin() (int, bool) {
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, false
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, false
	}
	return int(bytes / (1024 * 1024)), true
}
