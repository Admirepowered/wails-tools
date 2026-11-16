package sshmgr

import (
	"strings"
	"testing"
)

func TestParseStaticAndDynamic(t *testing.T) {
	static := "HOST::VM-0-6-ubuntu\nCPU:: Intel(R) Xeon(R) Platinum 8255C CPU @ 2.50GHz\nCORES::2\nUP::15473195\nDISK::/dev/vda2         51485392 29465708  19801572      60% /"
	dyn := "MEM::MemTotal:        2014988 kB MemAvailable:    1210100 kB \nLOAD::0.03\nSTAT1::cpu  262143761 271686 378457431 2392967426 9818435 0 2997736 0 0 0\nSTAT2::cpu  262143762 271686 378457432 2392967505 9818435 0 2997736 0 0 0\nGPU::"
	info := SystemInfo{}
	for _, line := range strings.Split(static, "\n") {
		applyKV(&info, line)
	}
	for _, line := range strings.Split(dyn, "\n") {
		applyKV(&info, line)
	}
	t.Logf("host=%q cpu=%q cores=%d up=%q disk=%d/%d %.0f%% mem=%d/%d load=%.2f cpu%%=%.0f",
		info.Hostname, info.CPUModel, info.CPUCores, info.Uptime,
		info.DiskUsedMB, info.DiskTotalMB, info.DiskPct,
		info.MemUsedMB, info.MemTotalMB, info.Load1, info.CPUPct)
	if info.DiskTotalMB == 0 {
		t.Fatal("disk not parsed")
	}
	if info.MemTotalMB == 0 {
		t.Fatal("memory not parsed")
	}
}

