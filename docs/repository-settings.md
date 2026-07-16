# Repository settings rollout

The repository is configured for squash-only merges, PR-title squash commit
titles, automatic branch deletion, update-branch support, Issues enabled, and
Wiki/Discussions disabled.

After this change reaches GitHub and every named check has completed once,
create the `main` ruleset. Applying it earlier can lock the only maintainer out
because GitHub cannot select checks that have never reported.

Protect `main` with these rules:

- restrict deletion and non-fast-forward updates;
- require a pull request, zero approving reviews, resolved conversations, and
  the branch to be current before merge;
- require `PR title / conventional`, every `CI` job, and any organization-wide
  security checks;
- allow only squash merges;
- grant `@jahwag` an emergency bypass and document each use.

Create a separate active tag ruleset targeting `refs/tags/v*` that prevents
updates and deletion, with the same emergency owner bypass. Enable Dependabot
alerts/security updates and private vulnerability reporting when the
organization plan exposes those controls.

Upload `assets/brand/social/zestmem-social.png` as the repository social preview
only after `make brand-verify` passes. GitHub does not currently expose social
preview upload through its repository API.
