import json


def host_fetch(address: str, port: int, marker: str = "foundation-network-ok") -> str:
    return (
        f"curl --noproxy '*' -g -fsS --connect-timeout 2 --max-time 4 "
        f"http://{address}:{port} | grep -x {marker}"
    )


def container_fetch(name: str, address: str, port: int) -> str:
    return f"docker exec {name} wget -T 3 -q -O - http://{address}:{port}"


def check_policy() -> None:
    # Positive controls distinguish a denied path from an absent listener.
    for address in ["127.0.0.1", "[::1]"]:
        server.succeed(host_fetch(address, 18080))
        server.succeed(host_fetch(address, 18081))
        server.succeed(host_fetch(address, 8082, "foundation-egress-ok"))
    server.succeed(container_fetch("alpha-peer", "alpha", 8080) + " | grep -x foundation-network-ok")
    for address in ["192.168.10.2", "[fd00:10::2]"]:
        server.succeed(container_fetch("alpha", address, 8081) + " | grep -x foundation-egress-ok")
    for address in ["192.168.20.2", "[fd00:20::2]"]:
        server.fail(container_fetch("alpha", address, 8081))

    for address in ["192.168.10.1", "[fd00:10::1]", "192.168.20.1", "[fd00:20::1]"]:
        for port in [8082, 18080, 18081]:
            client.fail(host_fetch(address, port))
        client.fail(f"nc -z -w 2 {address.strip('[]')} 2375")

    for address in ["172.30.10.10", "[fd42:10::10]"]:
        client.fail(host_fetch(address, 8080))
        server.fail(container_fetch("beta", address, 8080))
    for address in ["172.30.20.10", "[fd42:20::10]"]:
        server.fail(container_fetch("alpha", address, 8080))
    for address in ["172.30.10.1", "192.168.20.1", "[fd00:20::1]"]:
        server.fail(container_fetch("alpha", address, 8082))


start_all()
server.wait_for_unit("docker.service")
server.wait_for_unit("fixture-host-http.service")
client.wait_for_unit("fixture-egress-http.service")
client.wait_until_succeeds("ping -6 -c 1 -W 3 fd00:10::1")
server.succeed("docker load -i /etc/foundation-fixture-image")

for name, suffix in [("alpha", 10), ("beta", 20)]:
    server.succeed(
        f"docker network create --ipv6 --subnet 172.30.{suffix}.0/24 "
        f"--subnet fd42:{suffix}::/64 "
        f"-o com.docker.network.bridge.name=vps-{suffix:010x} {name}"
    )

limits = "--cpus=0.5 --memory=64m --memory-swap=64m --pids-limit=32 --cap-drop=ALL --read-only"
for name, network, suffix, ports in [
    ("alpha", "alpha", 10, "-p 127.0.0.1:18080:8080 -p '[::1]:18080:8080'"),
    ("alpha-peer", "alpha", 11, ""),
    # Deliberately unsafe publishing exercises our FORWARD guard, not Compose admission.
    ("beta", "beta", 20, "-p 0.0.0.0:18081:8080 -p '[::]:18081:8080'"),
]:
    subnet = 10 if network == "alpha" else 20
    server.succeed(
        f"docker run -d --name {name} --network {network} "
        f"--ip 172.30.{subnet}.{suffix} --ip6 fd42:{subnet}::{suffix} "
        f"{limits} {ports} foundation-network-fixture:test"
    )
server.wait_until_succeeds(host_fetch("127.0.0.1", 18080))

# Ensure direct-routing denial is meaningful even when the peer installs routes.
client.succeed("ip route add 172.30.0.0/16 via 192.168.10.1")
client.succeed("ip -6 route add fd42::/32 via fd00:10::1")

with subtest("foundation constrains host input and Docker forwarding"):
    check_policy()

with subtest("firewall reload preserves Docker networking and isolation"):
    server.succeed("systemctl reload nftables.service")
    check_policy()

with subtest("Docker restart preserves origins and forwarding policy"):
    server.succeed("systemctl restart docker.service")
    server.wait_for_unit("docker.service")
    check_policy()

with subtest("runtime applies bounded logging and no-new-privileges defaults"):
    observed = json.loads(server.succeed("docker inspect alpha"))[0]["HostConfig"]
    assert observed["LogConfig"] == {"Type": "local", "Config": {"max-file": "3", "max-size": "10m"}}, observed
    server.succeed("docker exec alpha grep -E '^NoNewPrivs:[[:space:]]+1$' /proc/1/status")
