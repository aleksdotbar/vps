# Delivery checklist

Contract: [MVP requirements](../docs/spec.md).

## Deploy and recover

- Integrate build selection, secrets, native execution, routing and observed results.
- Remove redundant setup-history and deployment-state machinery.
- Verify deployment, failed replacement recovery and interrupted-operation reconciliation on a disposable target.
- Finish environment deployment/rollback controls and service observations.

## Install and release

- Verify the on-host install command (`panel install`) end to end on a disposable NixOS host.
- Verify preserved machine settings, private access, reboot readiness and independent recovery.
- Complete responsive UI, embedded assets and release packaging.
- Run relevant repository checks and one integrated backend review.

## Operator acceptance

With explicit target/account authorization:

- Install the panel and deploy the external stateless webhook bot.
- Verify staging/production isolation, secrets, routing, rollback and interruption recovery.
- Test desktop and phone flows.

## Integration

- Preserve unrelated edits and unfinished worktrees; integrate required source deliberately.
- Do not resurrect deleted package docs or old task reports.
- Keep verification evidence in delivery output and commits.
