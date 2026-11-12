package hostsetup

import (
	"os"
	"syscall"
)

func sshOwned(info os.FileInfo, uid uint32) bool {
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && (owner.Uid == 0 || owner.Uid == uid) && info.Mode().Perm()&0022 == 0
}
