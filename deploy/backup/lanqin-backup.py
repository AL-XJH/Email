#!/usr/bin/env python3
"""Shell/cron recovery sets. Deliberately independent of native age backups."""
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import signal
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid

FORMAT = b"lanqin-shell-backup-v2\n"
PAYLOAD = {"FORMAT", "manifest.json", "compose.json", "runtime.tar.gz",
           "lanqin-backup.py", "lanqin-backup.sh", "lanqin-restore.sh"}
WRAPPER = '''#!/usr/bin/env bash
set -euo pipefail
umask 077
exec python3 "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/lanqin-backup.py" %s "$@"
'''


class Failure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Failure(message)


def run(args, *, stdin=None, timeout=180):
    # Never echo commands, compose output, environment, or Redis values on failure.
    try:
        result = subprocess.run(args, input=stdin, text=True, capture_output=True,
                                timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise Failure(f"{args[0]} could not complete ({type(exc).__name__})") from None
    require(result.returncode == 0,
            f"{args[0]} failed (exit {result.returncode}); private command output suppressed")
    return result.stdout.strip()


def worker(image, mounts, script):
    name = "lanqin-shell-worker-" + uuid.uuid4().hex
    try:
        return run(["docker", "run", "--rm", "-i", "--name", name, "--network", "none",
                    *mounts, "--entrypoint", "/bin/sh", image, "-s"], stdin=script, timeout=180)
    finally:
        # Killing a timed-out Docker client does not necessarily stop its container.
        subprocess.run(["docker", "rm", "-f", name], text=True, capture_output=True, timeout=30)


def dump(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def relpath(value):
    require(isinstance(value, str) and value and not any(c in value for c in "\x00\n\r\\"),
            "invalid recovery path")
    p = PurePosixPath(value)
    require(not p.is_absolute() and all(x not in ("", ".", "..") for x in value.split("/")),
            "unsafe recovery path")
    return value


def absolute(value):
    p = Path(value)
    require(p.is_absolute(), "APP_DIR and BACKUP_DIR must be absolute paths")
    # Reject symlink ancestors, including a dangling final component.
    for item in [p, *p.parents]:
        require(not item.is_symlink(), "symlink destination/ancestor is forbidden")
    return p


def inspect(container):
    return json.loads(run(["docker", "inspect", container]))[0]


def config_digest(image_id):
    """Hash the original config blob, not the daemon-specific image .Id.

    Stream docker-save so classic and containerd exports need no on-disk image
    copy. Config JSON contains secrets; neither its bytes nor errors are logged.
    """
    process = subprocess.Popen(["docker", "image", "save", image_id],
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    configs = {}
    referenced = set()
    try:
        assert process.stdout is not None
        with tarfile.open(fileobj=process.stdout, mode="r|") as archive:
            for member in archive:
                if not member.isfile() or member.size > 4 * 1024 * 1024:
                    continue
                if member.name != "manifest.json" and not re.fullmatch(r"(?:[0-9a-f]{64}\.json|blobs/sha256/[0-9a-f]{64})", member.name):
                    continue
                stream = archive.extractfile(member)
                assert stream is not None
                content = stream.read()
                try:
                    value = json.loads(content)
                except (ValueError, UnicodeDecodeError):
                    continue
                if member.name == "manifest.json" and isinstance(value, list):
                    referenced.update(entry.get("Config") for entry in value if isinstance(entry, dict))
                elif isinstance(value, dict) and isinstance(value.get("rootfs"), dict) and "architecture" in value and "os" in value:
                    configs[member.name] = "sha256:" + hashlib.sha256(content).hexdigest()
        # Containerd exports may include attestation configs. Only the config
        # selected by docker-save's image manifest identifies the runnable image.
        require(process.wait(timeout=30) == 0 and len(referenced) == 1 and referenced <= configs.keys(),
                "cannot prove a unique image config identity")
        return configs[referenced.pop()]
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        if process.stdout is not None:
            process.stdout.close()


def image_identity(image):
    digests = sorted({entry.rsplit("@", 1)[-1] for entry in image.get("RepoDigests", [])
                      if isinstance(entry, str) and "@" in entry and
                      re.fullmatch(r"sha256:[0-9a-f]{64}", entry.rsplit("@", 1)[-1])})
    return {"version": 1, "repo_digests": digests,
            "platform": {key: image.get(key, "") for key in ("Os", "Architecture", "Variant")},
            "config_digest": None if digests else config_digest(image["Id"])}


def recovery_image(manifest):
    image = json.loads(run(["docker", "image", "inspect", manifest["image"]]))[0]
    expected = manifest.get("image_identity")
    if expected is None:
        # Legacy v2 recorded either a config ID or a manifest ID. Prove it by
        # exact local ID, RepoDigest, or the original config blob. Never infer
        # equivalence from a tag or waive a mismatch.
        legacy_id = manifest.get("image_id")
        require(isinstance(legacy_id, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", legacy_id),
                "legacy v2 image identity is invalid")
        repo_ids = {value.rsplit("@", 1)[-1] for value in image.get("RepoDigests", [])}
        require(image["Id"] == legacy_id or legacy_id in repo_ids or
                config_digest(image["Id"]) == legacy_id,
                "legacy v2 image identity cannot be proven; obtain the original image or create a new backup")
    else:
        require(isinstance(expected, dict) and expected.get("version") == 1,
                "invalid stable image identity")
        current = image_identity(image)
        require(expected.get("platform") == current["platform"], "required image platform changed")
        digests = expected.get("repo_digests")
        require(isinstance(digests, list) and all(isinstance(d, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", d) for d in digests),
                "invalid stable image digest list")
        if digests:
            require(bool(set(digests) & set(current["repo_digests"])),
                    "required image content changed; stable registry digest mismatch")
        else:
            digest = expected.get("config_digest")
            require(isinstance(digest, str) and re.fullmatch(r"sha256:[0-9a-f]{64}", digest),
                    "missing stable image config digest")
            require(config_digest(image["Id"]) == digest,
                    "required image content changed; config digest mismatch")
    # Execute by the destination daemon's immutable local identity, never by the
    # source daemon's .Id (which may refer to another type of OCI object).
    return image["Id"]


def utc():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def compose_command(app, files):
    return ["docker", "compose", "--project-directory", str(app), "--env-file", str(app / ".env"),
            *[x for file in files for x in ("-f", str(file))]]


def check_config(config):
    require(set(config.get("services", {})) == {"lanqin-email"},
            "only the single-container lanqin-email deployment is supported")
    for network in config.get("networks", {}).values():
        require(not network.get("external"), "external networks require a separate recovery plan")
    service = config["services"]["lanqin-email"]
    for mount in service.get("volumes", []):
        require(mount.get("type") in ("bind", "tmpfs"),
                "volume/anonymous persistent volumes are unsupported; refusing an incomplete set")
    require(not service.get("devices") and not service.get("volumes_from"),
            "devices/volumes_from require a separate recovery plan")
    return service


# These workers run only against stopped source mounts or private restored files.
SNAPSHOT_WORKER = r'''set -eu
umask 077
test -f /data/lanqin.db
sqlite3 /data/lanqin.db ".backup '/snapshot/lanqin.db'"
chmod 600 /snapshot/lanqin.db
test "$(sqlite3 /snapshot/lanqin.db 'PRAGMA quick_check;')" = ok
test "$(sqlite3 /snapshot/lanqin.db "SELECT count(*) FROM sqlite_master WHERE type='table';")" -gt 0
cp -a /data/redis /snapshot/redis-work
cleanup() { redis-cli -p 16379 shutdown nosave >/dev/null 2>&1 || true; }
trap cleanup EXIT
redis-server /etc/redis-lanqin.conf --dir /snapshot/redis-work --port 16379 --bind 127.0.0.1 --daemonize yes --logfile /snapshot/redis.log --pidfile /snapshot/redis.pid --dbfilename snapshot.rdb
n=0
until test "$(redis-cli -p 16379 ping 2>/dev/null)" = PONG; do
 n=$((n+1)); test "$n" -lt 100; sleep .1
done
test "$(redis-cli -p 16379 save)" = OK
redis-cli -p 16379 --raw info keyspace > /snapshot/redis-keyspace.txt
redis-cli -p 16379 shutdown nosave
trap - EXIT
redis-check-rdb /snapshot/redis-work/snapshot.rdb >/dev/null
cp /snapshot/redis-work/snapshot.rdb /snapshot/redis-dump.rdb
chmod 600 /snapshot/redis-dump.rdb
'''

RESTORE_WORKER = r'''set -eu
umask 077
mkdir -p /data/redis
redis-check-rdb /restore/dump.rdb >/dev/null
cleanup() { redis-cli -p 16379 shutdown nosave >/dev/null 2>&1 || true; }
trap cleanup EXIT
start() {
 redis-server /etc/redis-lanqin.conf --dir /restore --port 16379 --bind 127.0.0.1 --daemonize yes --logfile /restore/recovery.log --pidfile /restore/recovery.pid --appendonly "$1" --enable-debug-command yes
 n=0
 until test "$(redis-cli -p 16379 ping 2>/dev/null)" = PONG; do
  n=$((n+1)); test "$n" -lt 100; sleep .1
 done
}
fingerprint() {
 redis-cli -p 16379 --raw debug digest
 redis-cli -p 16379 --raw info keyspace | tr -d '\r' | grep '^db' | sed 's/,avg_ttl=.*//' || test "$(redis-cli -p 16379 dbsize)" = 0
}
# RDB must be loaded with AOF disabled. Starting with appendonly=yes ignores it.
start no
fingerprint > /restore/.expected
# Enabling AOF starts the initial rewrite; wait for a complete manifest/base.
test "$(redis-cli -p 16379 config set appendonly yes)" = OK
n=0
while :; do
 info="$(redis-cli -p 16379 info persistence | tr -d '\r')"
 if printf '%s\n' "$info" | grep -q '^aof_rewrite_in_progress:0$' &&
    printf '%s\n' "$info" | grep -q '^aof_rewrite_scheduled:0$' &&
    printf '%s\n' "$info" | grep -q '^aof_last_bgrewrite_status:ok$'; then break; fi
 n=$((n+1)); test "$n" -lt 300; sleep .1
done
fingerprint > /restore/.actual
cmp /restore/.expected /restore/.actual
redis-cli -p 16379 shutdown nosave
# Prove the next ordinary appendonly=yes start reads exactly the loaded state.
start yes
fingerprint > /restore/.actual
cmp /restore/.expected /restore/.actual
redis-cli -p 16379 shutdown nosave
trap - EXIT
rm -f /restore/.expected /restore/.actual /restore/recovery.pid /restore/recovery.log
'''


def backup():
    app = absolute(os.environ.get("APP_DIR", "/opt/lanqin-email"))
    base = absolute(os.environ.get("BACKUP_DIR", "/root/lanqin-backups"))
    keep = int(os.environ.get("KEEP_DAYS", "14"))
    require(keep >= 1, "KEEP_DAYS must be at least 1")
    require(app.is_dir() and (app / ".env").is_file(), "APP_DIR/.env is required")
    require(not base.is_relative_to(app) and not app.is_relative_to(base),
            "BACKUP_DIR must be outside APP_DIR (no recursive backup roots)")
    base.mkdir(parents=True, exist_ok=True, mode=0o700)
    base.chmod(0o700)
    lock = (base / ".backup.lock").open("a")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        raise Failure("another backup is running") from None
    container = os.environ.get("CONTAINER", "lanqin-email-lanqin-email-1")
    source = inspect(container)
    require(source["State"]["Running"] and not source["State"].get("Paused"),
            "source container must be running and unpaused")
    source_image = json.loads(run(["docker", "image", "inspect", source["Image"]]))[0]
    stable_image_identity = image_identity(source_image)
    labels = source["Config"].get("Labels") or {}
    files = [Path(p) for p in labels.get("com.docker.compose.project.config_files", "").split(",") if p]
    if not files:
        files = [app / "docker-compose.yml"]
        if (app / "docker-compose.override.yml").exists():
            files.append(app / "docker-compose.override.yml")
    require(all(p.is_absolute() and p.is_file() for p in files), "compose source files are missing")
    config = json.loads(run(compose_command(app, files) + ["config", "--format", "json"]))
    service = check_config(config)
    live_env = dict(item.split("=", 1) for item in source["Config"].get("Env", []) if "=" in item)
    expected_env = service.get("environment", {})
    require(all(live_env.get(key) == value for key, value in expected_env.items()),
            "compose environment differs from running container; recreate it before backup (keys must stay paired with the database)")
    actual = {m["Destination"]: m for m in source["Mounts"]}
    mounts = []
    roots = {}

    def include(path):
        path = Path(os.path.abspath(path))
        require(path.exists(), "a runtime dependency is absent or has a broken symlink")
        require(not path.is_relative_to(base) and not base.is_relative_to(path),
                "runtime bind includes the shell backup root")
        if path.is_relative_to(app) and path != app:
            relative = path.relative_to(app).as_posix()
        else:
            require(path != app and path != Path("/"), "mounting APP_DIR or / is unsupported")
            relative = "_host/" + hashlib.sha256(str(path).encode()).hexdigest()[:16]
        relpath(relative)
        roots[str(path)] = relative
        return relative

    for mount in service.get("volumes", []):
        if mount["type"] != "bind":
            continue
        path = Path(mount["source"])
        live = actual.pop(mount["target"], None)
        require(live and live["Type"] == "bind" and
                Path(live["Source"]).resolve() == path.resolve(),
                "compose and live bind mounts differ; reconcile deployment before backing up")
        relative = include(path)
        # Single-file binds can still reference an unlinked old inode after host mv.
        if path.is_file():
            bound_inode = run(["docker", "exec", container, "stat", "-Lc", "%d:%i", mount["target"]])
            meta = path.stat()
            require(bound_inode == f"{meta.st_dev}:{meta.st_ino}",
                    "single-file bind inode differs from host file; recreate the source container first")
        mounts.append({"target": mount["target"], "path": relative,
                       "kind": "directory" if path.is_dir() else "file"})
    require(not actual, "unaccounted live mounts; refusing an incomplete set")
    mapping = {m["target"]: m["path"] for m in mounts}
    require("/data" in mapping and "/var/mail/vhosts" in mapping and
            "/var/lib/rspamd/dkim" in mapping, "required data/mail/DKIM bind mounts are missing")
    require(Path(next(p for p, r in roots.items() if r == mapping["/data"])) == app / "data",
            "this recovery format requires APP_DIR/data mounted at /data")
    require((app / "data/lanqin.db").is_file() and (app / "data/redis").is_dir(),
            "SQLite/Redis runtime data is missing")
    # Only the standard embedded Redis layout/config is supported, fail closed otherwise.
    redis_dir = run(["docker", "exec", container, "redis-cli", "--raw", "config", "get", "dir"]).splitlines()
    redis_aof = run(["docker", "exec", container, "redis-cli", "--raw", "config", "get", "appendonly"]).splitlines()
    require(redis_dir == ["dir", "/data/redis"] and redis_aof == ["appendonly", "yes"],
            "requires embedded Redis /data/redis with appendonly=yes")
    for file in [app / ".env", *files]:
        include(file)
    for name in ("docker-compose.override.yml", "docker-compose.override.yaml", "compose.override.yml", "compose.override.yaml"):
        if (app / name).exists():
            include(app / name)
    for category in ("configs", "secrets"):
        for entry in config.get(category, {}).values():
            require(not entry.get("external"), "external configs/secrets are unsupported")
            if "file" in entry:
                include(Path(entry["file"]))
    # Required runtime files can also be present beside an older compose definition.
    for name in ("nginx", "certs", "dkim", "acme-webroot"):
        if (app / name).exists():
            include(app / name)
    native = [app / "backups" / name for name in ("work", "artifacts", "restore")]
    # These are recreated EMPTY, including separately bind-mounted native directories.
    excluded_native = {str(p): include(p) for p in native if p.exists()}
    stamp = dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    target = base / stamp
    require(not target.exists(), "backup timestamp already exists; retry next second")
    partial = Path(tempfile.mkdtemp(prefix=".incomplete-", dir=base))
    partial.chmod(0o700)
    stopped = False
    completed = False
    try:
        snapshot = partial / ".snapshot"
        snapshot.mkdir(mode=0o700)
        # Set flag first: a timed-out stop may nevertheless have stopped the container.
        stopped = True
        run(["docker", "stop", "--time", "120", container], timeout=150)
        stop_state = inspect(container)["State"]
        require(not stop_state["Running"], "source container is still running")
        require(not stop_state.get("OOMKilled") and stop_state.get("ExitCode") in (0, 143),
                "source container did not stop cleanly; no consistent recovery set can be claimed")
        boundary_start = utc()
        # Postfix's container-layer spool is not a host bind. Never silently lose
        # mail already accepted by SMTP but still queued outside maildir/SQLite.
        spool = snapshot / "postfix-spool"
        run(["docker", "cp", container + ":/var/spool/postfix", str(spool)])
        for queue in ("active", "incoming", "deferred", "hold", "maildrop", "corrupt", "saved"):
            require(not (spool / queue).exists() or
                    not any(p.is_file() for p in (spool / queue).rglob("*")),
                    "Postfix has queued mail in the container layer; resume/drain it before backup")
        shutil.rmtree(spool)
        worker(source["Image"], ["--volumes-from", container + ":ro", "--mount",
                                  f"type=bind,source={snapshot},target=/snapshot"], SNAPSHOT_WORKER)
        shutil.rmtree(snapshot / "redis-work")
        seen = set()
        fingerprints = {}
        data = mapping["/data"]
        redis_path = data + "/redis"
        native_paths = set(excluded_native.values())

        def skip(name):
            if name in (data + "/lanqin.db", data + "/lanqin.db-wal", data + "/lanqin.db-shm"):
                return True
            if name.startswith(redis_path + "/"):
                return True
            if any(name.startswith(p + "/") for p in native_paths):
                return True
            # Historical rollback databases are not runtime dependencies.
            if name.startswith(data + "/lanqin.db.") or name.startswith(data + "/.lanqin-backup-"):
                return True
            return False

        def add(tar, path, name, ancestors=()):
            relpath(name)
            if name in seen or skip(name):
                return
            resolved = path.resolve(strict=True)
            require(resolved not in ancestors, "symlink directory cycle in runtime files")
            require(not resolved.is_relative_to(base), "symlink enters backup output")
            meta = path.stat()
            require(stat.S_ISDIR(meta.st_mode) or stat.S_ISREG(meta.st_mode),
                    "runtime tree contains a socket/device/FIFO; cannot make a complete set")
            info = tarfile.TarInfo("root/" + name)
            info.mode, info.uid, info.gid, info.mtime = stat.S_IMODE(meta.st_mode), meta.st_uid, meta.st_gid, meta.st_mtime
            seen.add(name)
            fingerprints[str(path)] = (meta.st_dev, meta.st_ino, meta.st_size, meta.st_mtime_ns, meta.st_ctime_ns)
            if path.is_dir():
                info.type = tarfile.DIRTYPE
                tar.addfile(info)
                if name not in native_paths:
                    for child in sorted(path.iterdir()):
                        add(tar, child, name + "/" + child.name, (*ancestors, resolved))
            else:
                info.size = meta.st_size
                with path.open("rb") as stream:
                    tar.addfile(info, stream)
                fingerprints[str(path)] = (meta.st_dev, meta.st_ino, meta.st_size, meta.st_mtime_ns, meta.st_ctime_ns)
                now = path.stat()
                require(fingerprints[str(path)] == (now.st_dev, now.st_ino, now.st_size, now.st_mtime_ns, now.st_ctime_ns),
                        "runtime file changed while archived; no recovery set was published")

        with tarfile.open(partial / "runtime.tar.gz", "w:gz", dereference=True) as tar:
            for path, relative in sorted(roots.items(), key=lambda item: (len(Path(item[0]).parts), item[0])):
                add(tar, Path(path), relative)
            for path, relative in ((snapshot / "lanqin.db", data + "/lanqin.db"),
                                   (snapshot / "redis-dump.rdb", redis_path + "/dump.rdb")):
                info = tar.gettarinfo(str(path), "root/" + relative)
                original = app / "data" / ("redis" if relative.endswith("dump.rdb") else "lanqin.db")
                meta = original.stat()
                info.uid, info.gid, info.mode = meta.st_uid, meta.st_gid, 0o600
                with path.open("rb") as stream:
                    tar.addfile(info, stream)
        for path, before in fingerprints.items():
            after = Path(path).stat()
            require(before == (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns),
                    "runtime file changed during backup; no recovery set was published")
        require(not inspect(container)["State"]["Running"], "source restarted during backup")
        # Config must not change across discovery and the stopped archive window.
        again = json.loads(run(compose_command(app, files) + ["config", "--format", "json"]))
        require(config == again, "compose/environment changed during backup")
        manifest = {"format": 2, "created": utc(), "source_app_dir": str(app),
                    "image": source["Config"]["Image"], "image_id": source["Image"],
                    "image_identity": stable_image_identity,
                    "consistency": "cold: sole application container stopped before both snapshots and all archives",
                    "stopped_at": boundary_start, "archive_finished_at": utc(),
                    "mounts": mounts, "paths": roots, "data_path": data,
                    "redis_path": redis_path, "native_empty_paths": sorted(native_paths),
                    "excluded": ["live SQLite WAL/SHM", "live Redis AOF (replaced by consistent RDB)",
                                 "historical lanqin.db.* rollback files", "native backup work/artifacts/restore contents"],
                    "redis_snapshot_keyspace": (snapshot / "redis-keyspace.txt").read_text()}
        dump(partial / "manifest.json", manifest)
        dump(partial / "compose.json", config)
        (partial / "FORMAT").write_bytes(FORMAT)
        shutil.copyfile(Path(__file__), partial / "lanqin-backup.py")
        for command in ("backup", "restore"):
            (partial / f"lanqin-{command}.sh").write_text(WRAPPER % command)
        shutil.rmtree(snapshot)
        for file in partial.iterdir():
            file.chmod(0o600)
        (partial / "SHA256SUMS").write_text("".join(f"{sha(partial / n)}  {n}\n" for n in sorted(PAYLOAD)))
        (partial / "SHA256SUMS").chmod(0o600)
        verify(partial)
        run(["docker", "start", container])
        stopped = False
        require(inspect(container)["State"]["Running"], "source did not resume")
        partial.rename(target)
        completed = True
        # Delete only verified sets of our own format, never unrelated date-named directories.
        cutoff = time.time() - keep * 86400
        for candidate in base.iterdir():
            if re.fullmatch(r"20\d{6}-\d{6}", candidate.name) and candidate.is_dir() and not candidate.is_symlink() and candidate.stat().st_mtime < cutoff:
                try:
                    verify(candidate)
                except (Failure, OSError, ValueError):
                    continue
                shutil.rmtree(candidate)
        print(f"backup done: {target}; cold snapshot, SHA256 verified, source resumed")
    finally:
        if stopped:
            # Do not claim success when resume fails; preserve the error as nonzero.
            run(["docker", "start", container])
        if not completed and partial.exists():
            shutil.rmtree(partial)


def verify(backup_dir):
    require(backup_dir.is_dir() and not backup_dir.is_symlink(), "backup directory must be real")
    require({p.name for p in backup_dir.iterdir()} == PAYLOAD | {"SHA256SUMS"}, "unexpected/missing recovery set files")
    for name in PAYLOAD | {"SHA256SUMS"}:
        p = backup_dir / name
        require(p.is_file() and not p.is_symlink(), "backup components must be regular files")
    require((backup_dir / "FORMAT").read_bytes() == FORMAT, "unsupported backup format (old sets cannot be safely auto-restored)")
    lines = (backup_dir / "SHA256SUMS").read_text().splitlines()
    found = {}
    for line in lines:
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        if match is None:
            raise Failure("invalid SHA256SUMS format")
        digest, name = match.groups()
        require(name in PAYLOAD and name not in found, "unexpected/duplicate checksum name")
        found[name] = digest
    require(set(found) == PAYLOAD, "incomplete checksum list")
    for name, digest in found.items():
        require(sha(backup_dir / name) == digest, "SHA256 mismatch; restore refused")
    manifest = json.loads((backup_dir / "manifest.json").read_text())
    require(manifest.get("format") == 2, "invalid manifest version")
    return manifest


def restore(backup_dir, app):
    manifest = verify(backup_dir)
    app = absolute(app)
    require(len(app.parts) >= 3, "APP_DIR must be a specific application directory")
    require(not app.exists() or (app.is_dir() and not any(app.iterdir())),
            "APP_DIR must be absent or empty; stop containers and move the old directory aside first")
    require(not backup_dir.resolve().is_relative_to(app), "backup must be outside APP_DIR")
    for cid in run(["docker", "ps", "-q"]).splitlines():
        for mount in inspect(cid)["Mounts"]:
            source = Path(mount["Source"])
            require(not source.is_relative_to(app) and not app.is_relative_to(source),
                    "a running container uses APP_DIR; stop it before restore")
    config = json.loads((backup_dir / "compose.json").read_text())
    service = check_config(config)
    image_id = recovery_image(manifest)
    data = relpath(manifest["data_path"])
    redis_path = relpath(manifest["redis_path"])
    require(redis_path == data + "/redis", "invalid Redis snapshot path")
    for original, relative in manifest["paths"].items():
        relpath(relative)
        require(Path(original).is_absolute(), "invalid original dependency path")
    for mount in manifest["mounts"]:
        relpath(mount["path"])
        require(mount["kind"] in ("file", "directory"), "invalid mount type")
    # Validate all archive headers before creating any output. Never extract links/devices.
    with tarfile.open(backup_dir / "runtime.tar.gz", "r:gz") as tar:
        members = tar.getmembers()
        names = set()
        for member in members:
            require(member.name.startswith("root/"), "archive entry outside recovery root")
            name = relpath(member.name[5:])
            require(name not in names, "duplicate archive entry")
            names.add(name)
            require(member.isdir() or member.isfile(), "archive links/devices are forbidden")
            require(member.isdir() or not member.mode & 0o7000,
                    "setuid/setgid file mode is forbidden")
        require(data + "/lanqin.db" in names and redis_path + "/dump.rdb" in names and ".env" in names,
                "missing database/Redis/environment snapshot")
        app.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        staging = Path(tempfile.mkdtemp(prefix=".lanqin-restore-", dir=app.parent))
        staging.chmod(0o700)
        try:
            # Extract manually: no tar symlink following, no absolute names, no overwrite.
            directories = []
            for member in members:
                out = staging / member.name[5:]
                out.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
                if member.isdir():
                    out.mkdir(exist_ok=True, mode=0o700)
                    directories.append((out, member))
                else:
                    content = tar.extractfile(member)
                    require(content is not None, "archive file content is absent")
                    assert content is not None
                    with out.open("xb") as stream, content:
                        shutil.copyfileobj(content, stream)
                    os.chown(out, member.uid, member.gid)
                    out.chmod(member.mode)
            for mount in manifest["mounts"]:
                out = staging / mount["path"]
                require(out.is_file() if mount["kind"] == "file" else out.is_dir(),
                        "restored bind source type mismatch")
            for mount in service.get("volumes", []):
                if mount["type"] == "bind":
                    require(mount["source"] in manifest["paths"], "unmapped bind source")
                    mount["source"] = str(app / manifest["paths"][mount["source"]])
                    mount.setdefault("bind", {})["create_host_path"] = False
            for category in ("configs", "secrets"):
                for entry in config.get(category, {}).values():
                    if "file" in entry:
                        require(entry["file"] in manifest["paths"], "unmapped config/secret file")
                        entry["file"] = str(app / manifest["paths"][entry["file"]])
            config.pop("name", None)
            service.pop("build", None)
            service["image"] = manifest["image"]
            dump(staging / "docker-compose.restore.json", config)
            # Parse the actual config users will start, without printing resolved secrets.
            run(["docker", "compose", "--project-directory", str(app), "-f",
                 str(staging / "docker-compose.restore.json"), "config", "--quiet"])
            redis = staging / redis_path
            require(not any(p.name != "dump.rdb" for p in redis.iterdir()), "unexpected Redis files in archive")
            worker(image_id, ["--mount", f"type=bind,source={redis},target=/restore"], RESTORE_WORKER)
            # Full SQLite integrity check in the original image (no host SQLite dependency).
            worker(image_id, ["--mount", f"type=bind,source={staging / data},target=/check,readonly"],
                   "test \"$(sqlite3 /check/lanqin.db 'PRAGMA integrity_check;')\" = ok")
            redis_meta = next(m for out, m in directories if out == redis)
            for path in redis.rglob("*"):
                os.chown(path, redis_meta.uid, redis_meta.gid)
            for path, meta in sorted(directories, key=lambda item: len(item[0].parts), reverse=True):
                os.chown(path, meta.uid, meta.gid)
                path.chmod(meta.mode)
            (staging / ".env").chmod(0o600)
            # Keep a private provenance/checksum record outside the Compose runtime.
            dump(staging / ".lanqin-recovery.json", manifest)
            if app.exists():
                app.rmdir()  # empty only; never delete/overlay existing application files
            staging.rename(app)
            print(f"restore done: {app}; all SHA256 verified; SQLite integrity ok; Redis RDB→AOF→restart digest/keyspace verified")
            print(f"Start explicitly: docker compose --project-directory '{app}' -f '{app}/docker-compose.restore.json' up -d --no-build")
        finally:
            if staging.exists():
                shutil.rmtree(staging)


def main():
    os.umask(0o077)
    def interrupted(signum, _frame):
        # The surrounding finally blocks clean workers and restart a stopped source.
        signal.signal(signum, signal.SIG_IGN)
        raise Failure("operation interrupted")
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("backup", "restore", "verify"))
    parser.add_argument("--backup-dir")
    parser.add_argument("--app-dir")
    args = parser.parse_args()
    require(os.geteuid() == 0, "run as root to preserve mail ownership and access Docker")
    if args.command == "backup":
        backup()
    else:
        require(args.backup_dir is not None, "--backup-dir is required")
        directory = absolute(args.backup_dir)
        if args.command == "verify":
            verify(directory)
            print("recovery set: all SHA256 verified")
        else:
            require(args.app_dir is not None, "--app-dir is required (there is no restore default)")
            restore(directory, args.app_dir)


if __name__ == "__main__":
    try:
        main()
    except (Failure, OSError, ValueError, KeyError, tarfile.TarError) as error:
        # OSError/JSON exceptions may embed secret material; emit only curated Failure text.
        print(f"ERROR: {error if isinstance(error, Failure) else type(error).__name__}; operation failed closed", file=sys.stderr)
        sys.exit(1)
