module github.com/aleksdotbar/vps/panel/tools

go 1.27.3

require (
	github.com/klauspost/compress v1.20.0 // indirect
	go.uber.org/nilaway v0.0.0-20260918162853-acb8859b9031 // indirect
	golang.org/x/exp/typeparams v0.0.0-20260908205506-85c1c2202aba // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)

tool (
	go.uber.org/nilaway/cmd/nilaway
	golang.org/x/vuln/cmd/govulncheck
)
