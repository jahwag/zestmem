# Encrypted GitHub backups

ZestMem's PostgreSQL volume survives container and host restarts, but it does not protect against disk loss, accidental volume deletion, or loss of the host. The optional off-site path uploads only an age-encrypted, restore-tested dump to a separate private GitHub repository.

Do not commit dumps to Git history. Release assets are replaceable binary artifacts and can be retention-managed without permanently inflating a clone.

## Security model

- `backup.sh` first creates a PostgreSQL custom-format dump and restores it into a fresh temporary database. Upload never runs for an unverified `.partial` file.
- Encryption happens locally with an age public recipient before any GitHub request.
- GitHub receives no plaintext dump or private age identity.
- Use a fine-grained token restricted to **Contents: read and write** on one private backup repository.
- Store the age private identity off-host in an existing encrypted secret store. A key held only on the database host is not disaster recovery.
- The upload script sends the GitHub token through curl configuration on standard input, not a command-line argument.

GitHub-account compromise can delete both releases and repository secrets. For high-assurance installations, replicate encrypted assets to a second provider or immutable storage account under separate credentials.

## Configure

Create a dedicated private repository:

```sh
gh repo create OWNER/zestmem-backups --private --disable-issues --disable-wiki --add-readme
```

Generate an age identity on a trusted recovery workstation, not only on the ZestMem host:

```sh
umask 077
age-keygen -o zestmem-backup.agekey
age-keygen -y zestmem-backup.agekey
```

The second command prints the public `age1…` recipient. Preserve `zestmem-backup.agekey` in an encrypted secret manager and test that another trusted recovery environment can retrieve it.

Set these values in the deployment's protected `.env` file:

```dotenv
GITHUB_BACKUP_REPOSITORY=OWNER/zestmem-backups
GITHUB_BACKUP_RETENTION_DAYS=30
GITHUB_TOKEN=github_pat_FINE_GRAINED_TOKEN
AGE_RECIPIENT=age1PUBLIC_RECIPIENT
```

Keep the file mode `0600`. Start or recreate the backup service:

```sh
podman compose up -d backup
podman compose exec backup /usr/local/bin/backup.sh
```

A successful run writes `/backups/last-success` after local restore verification and `/backups/last-offsite-success` after GitHub accepts the encrypted asset. It also emits `offsite_backup_uploaded` with the release tag, asset name, and encrypted SHA-256 digest. A failed run retries hourly until the complete restore-and-upload path succeeds.

All core Compose services use `restart: unless-stopped`. Enable the container engine's boot restart facility as appropriate for the host; for rootful Podman on systemd:

```sh
systemctl enable --now podman-restart.service
```

## Monitor

Check both success markers and the running service:

```sh
podman compose ps backup
podman compose exec backup sh -c 'cat /backups/last-success /backups/last-offsite-success'
gh release list --repo OWNER/zestmem-backups --limit 5
```

Alert when either marker is older than the expected backup interval. Local plaintext dumps follow `BACKUP_RETENTION_DAYS`; encrypted local files and GitHub releases follow `GITHUB_BACKUP_RETENTION_DAYS`.

## Restore drill

Download one encrypted asset on a trusted recovery machine and verify its published digest before decryption:

```sh
gh release download TAG --repo OWNER/zestmem-backups --pattern '*.dump.age'
sha256sum memory-*.dump.age
age --decrypt --identity zestmem-backup.agekey --output memory.dump memory-*.dump.age
pg_restore --list memory.dump >/dev/null
```

`pg_restore --list` verifies archive readability, not database consistency. The full drill restores into a fresh PostgreSQL database and runs ZestMem's structural/count checks:

```sh
# Place memory.dump in the deployment's backups volume, then:
podman compose exec backup /usr/local/bin/restore-test.sh /backups/memory.dump
```

Perform this drill after initial setup, after key rotation, and periodically. A backup that has never been restored is an unverified hope.

## Recovery

For disaster recovery, provision the same or newer supported PostgreSQL/pgvector version, decrypt the selected asset on the recovery host, restore it with `pg_restore --exit-on-error --no-owner --no-acl`, then start the matching ZestMem release. Keep the damaged volume untouched until recall, grants, revision counts, and embeddings have been verified on the recovered service.
