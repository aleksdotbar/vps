# Build only native SSH/sudo policy fixtures. No accounts, keys, service startup
# or activation: qualified config parsing checks our Match scoping and fixed rule.
{ system, final ? false }:
let
  c = system.config;
  pkgs = system.pkgs;
  lib = pkgs.lib;
  operator = builtins.head (builtins.filter (name: name != "root" && name != "panel-runner") c.services.openssh.settings.AllowUsers);
  canEvaluate = instance: (builtins.tryEval instance.config.system.build.toplevel.drvPath).success;
  refuses = module: !canEvaluate (system.extendModules { modules = [ module ]; });
in
assert lib.assertMsg (canEvaluate system) "The executor role must pass native admission.";
assert lib.assertMsg (lib.all refuses ([
  { users.users.panel-runner.extraGroups = [ "wheel" ]; }
  { users.groups.docker.members = [ "panel-runner" ]; }
  { security.sudo.extraRules = [ { users = [ "panel-runner" ]; commands = [ "ALL" ]; } ]; }
  { security.sudo.extraRules = lib.mkForce (builtins.filter (rule: !(builtins.elem operator rule.users)) c.security.sudo.extraRules); }
  { security.sudo.wheelNeedsPassword = false; }
  { security.sudo.extraRules = [ { users = [ "ALL" ]; commands = [ "ALL" ]; } ]; }
  { security.sudo.extraRules = [ { users = [ "panel" ]; commands = [ "ALL" ]; } ]; }
] ++ lib.optional final {
  services.openssh.settings.AllowUsers = lib.mkForce [ "panel-runner" ];
})) "Runner group privileges, a missing final human operator and extra runner sudo authority must fail admission.";
pkgs.runCommand "executor-native-policy" {
  nativeBuildInputs = [ pkgs.openssh pkgs.sudo pkgs.python3 ];
  sshConfig = c.environment.etc."ssh/sshd_config".source;
  sudoConfig = c.environment.etc.sudoers.source;
} ''
  mkdir "$out"
  for address in 100.64.1.2 fd7a:115c:a1e0::2 192.0.2.5 2001:db8::5; do
    sshd -G -T -C "user=panel-runner,host=fixture,addr=$address,laddr=100.64.0.1,lport=22" -f "$sshConfig" > "$out/runner-$address"
  done
  sshd -G -T -C 'user=operator,host=fixture,addr=192.0.2.5,laddr=100.64.0.1,lport=22' -f "$sshConfig" > "$out/operator"
  sshd -G -T -C 'user=root,host=fixture,addr=192.0.2.5,laddr=100.64.0.1,lport=22' -f "$sshConfig" > "$out/root"
  visudo -c -f "$sudoConfig"
  cp "$sudoConfig" "$out/sudoers"
  python3 - "$out" <<'PY'
  import pathlib, sys
  root = pathlib.Path(sys.argv[1])
  def config(path):
      result = {}
      for line in path.read_text().splitlines():
          key, _, value = line.partition(' ')
          result.setdefault(key.lower(), []).append(value)
      return result
  expected = {
      'allowusers': ['panel-runner@100.64.0.0/10', 'panel-runner@fd7a:115c:a1e0::/48'],
      'authorizedkeysfile': '/var/lib/panel-access/panel-runner/authorized_keys',
      'authorizedkeyscommand': 'none', 'trustedusercakeys': 'none',
      'authorizedprincipalscommand': 'none', 'authorizedprincipalsfile': 'none',
      'authenticationmethods': 'publickey', 'pubkeyauthentication': 'yes',
      'passwordauthentication': 'no', 'kbdinteractiveauthentication': 'no',
      'hostbasedauthentication': 'no',
      'forcecommand': '/run/wrappers/bin/sudo -n -- /run/current-system/sw/bin/panel host-helper',
      'disableforwarding': 'yes', 'permittty': 'no', 'permituserrc': 'no',
      'permituserenvironment': 'no',
  }
  for path in root.glob('runner-*'):
      actual = config(path)
      for key, value in expected.items():
          assert actual.get(key) == (value if isinstance(value, list) else [value]), (path.name, key, actual.get(key))
  for user in ('operator', 'root'):
      actual = config(root / user)
      assert actual['forcecommand'] == ['none'], user
      assert actual['authenticationmethods'] == ['${c.services.openssh.settings.AuthenticationMethods}'], user
  rules = [line.split() for line in (root / 'sudoers').read_text().splitlines() if line.startswith('panel-runner ')]
  assert rules == [['panel-runner', 'ALL=(root:root)', 'NOPASSWD:NOSETENV:', '/run/current-system/sw/bin/panel', 'host-helper']], rules
  operators = [name for name in ${builtins.toJSON c.services.openssh.settings.AllowUsers} if name not in ('root', 'panel-runner')]
  assert len(operators) == 1, operators
  lines = (root / 'sudoers').read_text().splitlines()
  operator_rules = [line.split() for line in lines if line.startswith(operators[0] + ' ')]
  assert operator_rules == [[operators[0], 'ALL=(root:root)', 'NOPASSWD:NOSETENV:', 'ALL']], operator_rules
  assert not any(line.startswith('panel ') for line in lines), 'panel service gained sudo authority'
  assert not any(line.startswith('%wheel ') and 'NOPASSWD' in line for line in lines), 'wheel password gate removed'
  PY
''
