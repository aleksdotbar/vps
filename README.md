# Personal infrastructure panel

Self-hosted project deployment panel.

- Hosts: NixOS; private management over Tailscale.
- Applications: Docker Compose; builds in GitHub Actions; public ingress through Cloudflare Tunnel.
- Administration: one operator; browser access from desktop or phone.

## Documentation

- [Product requirements](docs/spec.md)
- [Development commands](docs/development.md)
- [Operator procedures](docs/operations.md)
- [Delivery checklist](tasks/todo.md)
- [Agent rules](AGENTS.md)
- [Project example](examples/stateless/README.md)
