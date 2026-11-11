import shlex


def ssh_command(user: str = "operator", address: str = "192.168.20.1") -> str:
    return (
        "ssh -o BatchMode=yes -o ConnectTimeout=4 -o IdentitiesOnly=yes "
        "-o StrictHostKeyChecking=yes -o UserKnownHostsFile=/root/known-hosts "
        f"-i /root/operator-key {user}@{address}"
    )


def check_access() -> None:
    client.succeed(ssh_command() + " 'sudo -n id -u' | grep -x 0")
    client.succeed(ssh_command(address="fd00:20::1") + " true")
    client.fail(ssh_command("stranger") + " true")
    client.fail(ssh_command("root") + " true")
    client.fail("nc -z -w 3 192.168.10.1 22")
    client.fail("nc -6 -z -w 3 fd00:10::1 22")


def check_network() -> None:
    server.succeed("curl -fsS --max-time 4 http://127.0.0.1:8080 | grep foundation-fixture-ok")
    server.succeed("curl --noproxy '*' -g -fsS --max-time 4 http://[::1]:8080 | grep foundation-fixture-ok")
    for address in ["192.168.10.2", "[fd00:10::2]"]:
        server.succeed(f"curl --noproxy '*' -fsS --max-time 4 http://{address}:8081 | grep foundation-fixture-ok")
    for address in ["192.168.10.1", "192.168.20.1", "[fd00:10::1]", "[fd00:20::1]"]:
        client.fail(f"curl --noproxy '*' -fsS --connect-timeout 2 --max-time 4 http://{address}:8080")


start_all()
server.wait_for_unit("fixture-http.service")
server.wait_for_unit("sshd.service")
client.wait_for_unit("fixture-http.service")
client.wait_until_succeeds("ping -6 -c 1 -W 3 fd00:10::1")

with subtest("operator access is private and unauthorized users are denied"):
    client.succeed("ssh-keygen -q -t ed25519 -N '' -f /root/operator-key")
    public_key = client.succeed("cat /root/operator-key.pub").strip()
    for user in ["operator", "stranger", "root"]:
        home = "/root" if user == "root" else f"/home/{user}"
        server.succeed(f"install -d -m 0700 -o {user} {home}/.ssh")
        server.succeed(f"printf '%s\\n' {shlex.quote(public_key)} > {home}/.ssh/authorized_keys")
        server.succeed(f"chown {user} {home}/.ssh/authorized_keys; chmod 0600 {home}/.ssh/authorized_keys")
    host_key = server.succeed("cat /etc/ssh/ssh_host_ed25519_key.pub").strip()
    known_host = f"192.168.20.1,fd00:20::1 {host_key}"
    client.succeed(f"printf '%s\\n' {shlex.quote(known_host)} > /root/known-hosts")
    check_access()

with subtest("live service ports stay blocked on public and management links"):
    check_network()
    server.succeed("systemctl reload nftables.service")
    check_access()
    check_network()

with subtest("stopping the firewall closes new SSH connections"):
    # The fixture is not part of the host profile's listening-service policy.
    server.succeed("systemctl stop fixture-http.service")
    server.succeed("systemctl stop nftables.service")
    for address in ["192.168.10.1", "fd00:10::1", "192.168.20.1", "fd00:20::1"]:
        client.fail(f"nc -z -w 3 {address} 22")
    server.succeed("systemctl start nftables.service sshd.service fixture-http.service")
    check_access()
    check_network()

with subtest("reapply and explicit recovery preserve the working policy"):
    baseline = server.succeed("readlink -f /run/current-system").strip()
    quoted = shlex.quote(baseline)
    server.succeed(f"nix-env --profile /nix/var/nix/profiles/system --set {quoted}")
    server.succeed(f"{quoted}/bin/switch-to-configuration test", timeout=120)
    check_access()
    check_network()
    server.succeed(f"{quoted}/specialisation/deny-operator/bin/switch-to-configuration test", timeout=120)
    client.succeed("nc -z -w 3 192.168.20.1 22")
    client.fail(ssh_command() + " true")
    server.succeed(f"nix-env --profile /nix/var/nix/profiles/system --set {quoted}")
    server.succeed(f"{quoted}/bin/switch-to-configuration switch", timeout=120)
    check_access()
    check_network()
    assert server.succeed("readlink -f /nix/var/nix/profiles/system").strip() == baseline

with subtest("cold restart retains host identity, logs and network policy"):
    server.succeed("logger foundation-before-restart; journalctl --sync")
    server.shutdown()
    server.start()
    server.wait_for_unit("fixture-http.service")
    server.wait_for_unit("sshd.service")
    assert server.succeed("cat /etc/ssh/ssh_host_ed25519_key.pub").strip() == host_key
    server.succeed("journalctl --no-pager | grep foundation-before-restart")
    check_access()
    check_network()
