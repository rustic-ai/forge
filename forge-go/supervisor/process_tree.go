package supervisor

import gopsprocess "github.com/shirou/gopsutil/v3/process"

func descendantPIDs(pid int) []int {
	seen := map[int32]struct{}{}
	out := make([]int, 0)

	var walk func(int32)
	walk = func(current int32) {
		proc, err := gopsprocess.NewProcess(current)
		if err != nil {
			return
		}
		children, err := proc.Children()
		if err != nil {
			return
		}
		for _, child := range children {
			if _, ok := seen[child.Pid]; ok {
				continue
			}
			seen[child.Pid] = struct{}{}
			walk(child.Pid)
			out = append(out, int(child.Pid))
		}
	}

	walk(int32(pid))
	return out
}
