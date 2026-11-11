import json

start_all()
server.wait_for_unit("docker.service")
server.wait_for_unit("docker-registry.service")
server.succeed("curl --fail http://127.0.0.1/v2/")
server.succeed("docker load --input /etc/panel-fixture-image")
server.succeed("docker tag panel-deployment-fixture:test ghcr.io/fixture/app:test")
server.succeed("docker push ghcr.io/fixture/app:test")
server.succeed("docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' ghcr.io/fixture/app:test | grep '^ghcr.io/fixture/app@sha256:' > /root/fixture-image")
server.succeed("nix-env --profile /nix/var/nix/profiles/system --set $(readlink -f /run/current-system)")
system = server.succeed("readlink -f /run/current-system").strip()
identity = {
    "installation_id": "11111111-1111-4111-8111-111111111111",
    "server_id": "22222222-2222-4222-8222-222222222222",
    "role": "panel",
}
receipt = {
    "operation_id": "44444444-4444-4444-8444-444444444444",
    "phase": "final_active",
    "confirmed_at": 100,
    "identity": identity,
    "final_system": system,
    "booted_system": system,
    "recovery_deadline": 101,
}
server.succeed("install -d -m 0700 /var/lib/panel-host /var/lib/panel-deploy")
server.succeed("umask 077; cat > /var/lib/panel-host/identity.json <<'JSON'\n" + json.dumps(identity, separators=(",", ":")) + "\nJSON")
# State records use canonical JSON without a trailing newline.
server.succeed("umask 077; printf '%s' '" + json.dumps(receipt, separators=(",", ":")) + "' > /var/lib/panel-host/final-receipt.json")
status, output = server.execute("PANEL_NATIVE_VM=1 /etc/panel-fixture-test -test.run '^TestNativeDeploymentAndRecovery$' -test.v -test.timeout=12m", timeout=780)
print(output)
if status:
    print(server.succeed("journalctl --no-pager --unit='panel-deploy-*' -n 40"))
    print(server.succeed("systemctl show docker.service nftables.service --property=Id,LoadState,ActiveState,SubState,MainPID,FragmentPath,DropInPaths,NeedDaemonReload"))
    print(server.succeed("time docker --host=unix:///run/docker.sock info --format '{{.ServerVersion}} {{.Architecture}} {{.CgroupVersion}} {{.CgroupDriver}} {{.LoggingDriver}} {{.LiveRestoreEnabled}} {{json .SecurityOptions}}'"))
    raise Exception("Native deployment/recovery failed")
