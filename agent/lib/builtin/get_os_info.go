package builtin

import (
	"encoding/json"
	"github.com/Jumpscale/agent2/agent/lib/pm"
	"github.com/Jumpscale/agent2/agent/lib/pm/core"
	"github.com/shirou/gopsutil/host"
)

const (
	cmdGetOsInfo = "get_os_info"
)

func init() {
	pm.CmdMap[cmdGetOsInfo] = InternalProcessFactory(getOsInfo)
}

func getOsInfo(cmd *core.Cmd, cfg pm.RunCfg) *core.JobResult {
	result := core.NewBasicJobResult(cmd)
	result.Level = pm.LevelResultJSON

	info, err := host.HostInfo()

	if err != nil {
		result.State = pm.StateError
		m, _ := json.Marshal(err)
		result.Data = string(m)
	} else {
		result.State = pm.StateSuccess
		m, _ := json.Marshal(info)

		result.Data = string(m)
	}

	return result
}
