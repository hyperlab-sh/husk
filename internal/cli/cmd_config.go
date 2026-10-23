package cli

import (
	"time"

	"github.com/hyperlab-sh/husk/pkg/protocol"
)

func configCommand() command {
	reload := serviceMethod("husk config reload", protocol.MethodConfigReload, nil, renderReports)
	reload.timeout = 10 * time.Second
	return command{usage: "husk config", verbs: map[string]command{"reload": reload}}
}
