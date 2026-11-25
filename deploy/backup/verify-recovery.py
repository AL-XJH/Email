#!/usr/bin/env python3
"""在专用 VM 上执行：合成单容器 fixture → 完整备份 → 空目录恢复 → 故障用例。
不使用/修改 demo 数据。默认保留证据目录，删除的仅是本脚本新建的容器。
"""
import argparse
import hashlib
import io
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import time


def command(args, *, env=None, fail=False, expected=None, timeout=240):
    result = subprocess.run(args, text=True, capture_output=True, env=env, timeout=timeout)
    if fail:
        assert result.returncode != 0, f"expected refusal: {args[0]}"
        if expected:
            assert expected in result.stderr, result.stderr
        print("PASS refusal:", result.stderr.strip())
    else:
        if result.returncode:
            raise RuntimeError(f"command failed: {args[0]}\n{result.stderr}\n{result.stdout}")
    return result.stdout.strip()


def checksum_set(directory):
    names = [line.split("  ")[1] for line in (directory / "SHA256SUMS").read_text().splitlines()]
    (directory / "SHA256SUMS").write_text("".join(
        f"{hashlib.sha256((directory / name).read_bytes()).hexdigest()}  {name}\n" for name in names))


def image_identity_cases(root, backup, tools, image):
    spec = importlib.util.spec_from_file_location("backup_identity", tools / "lanqin-backup.py")
    assert spec is not None and spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    actual = json.loads(command(["docker", "image", "inspect", image]))[0]
    config_id = module.config_digest(actual["Id"])
    original = json.loads((backup / "manifest.json").read_text())
    assert original["image_identity"]["repo_digests"], "registry fixture needs a stable RepoDigest"
    assert config_id != actual["Id"], "run this regression on the containerd test VM"

    def recovery_set(label, edit):
        path = root / label
        shutil.copytree(backup, path)
        manifest = json.loads((path / "manifest.json").read_text())
        edit(manifest)
        (path / "manifest.json").write_text(json.dumps(manifest))
        checksum_set(path)
        return path

    # A classic-engine backup has the config ID, but this VM executes by the
    # manifest ID. Keep registry identity unchanged and exercise real workers.
    cross = recovery_set("classic-engine-set", lambda m: m.update(image_id=config_id))
    destination = root / "classic-to-containerd-restore"
    print(command(["bash", str(tools / "lanqin-restore.sh"), "--backup-dir", str(cross),
                   "--app-dir", str(destination)]))
    assert (destination / "data/redis/appendonly").is_dir()
    print("PASS classic config-ID -> containerd manifest-ID recovery by stable RepoDigest; real Redis/SQLite workers")

    def legacy_edit(manifest):
        manifest.pop("image_identity")
        manifest["image_id"] = config_id
    legacy = recovery_set("legacy-classic-set", legacy_edit)
    assert module.recovery_image(json.loads((legacy / "manifest.json").read_text())) == actual["Id"]
    print("PASS legacy v2 classic config-ID proven by original config blob")
    def unverifiable_edit(manifest):
        manifest.pop("image_identity")
        manifest["image_id"] = "sha256:" + "0" * 64
    unknown = recovery_set("legacy-unprovable-set", unverifiable_edit)
    rejected = root / "image-must-stay-absent"
    command(["bash", str(tools / "lanqin-restore.sh"), "--backup-dir", str(unknown),
             "--app-dir", str(rejected)], fail=True, expected="legacy v2 image identity cannot be proven")
    assert not rejected.exists()

    # Retag only a private test alias; never mutate the build/demo/production tag.
    alias = "lanqin-backup-identity-test:" + str(os.getpid())
    command(["docker", "tag", image, alias])
    try:
        changed = recovery_set("retagged-set", lambda m: m.update(image=alias))
        assert module.recovery_image(json.loads((changed / "manifest.json").read_text())) == actual["Id"]
        command(["docker", "tag", "debian:trixie-slim", alias])
        command(["bash", str(tools / "lanqin-restore.sh"), "--backup-dir", str(changed),
                 "--app-dir", str(rejected)], fail=True, expected="stable registry digest mismatch")
        assert not rejected.exists()
        # Cover local-only config identity fallback without relying on RepoDigests.
        local = dict(actual, RepoDigests=[])
        identity = module.image_identity(local)
        assert identity["config_digest"] == config_id
        local_manifest = dict(original, image_identity=identity)
        assert module.recovery_image(local_manifest) == actual["Id"]
        local_manifest["image"] = alias
        try:
            module.recovery_image(local_manifest)
        except module.Failure as error:
            assert "config digest mismatch" in str(error)
        else:
            raise AssertionError("changed image incorrectly accepted by config fallback")
        print("PASS changed-content tag rejected by registry/config digests; destination absent")
    finally:
        command(["docker", "image", "rm", alias])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", default="/srv/fix117-backup")
    parser.add_argument("--image", default="lanqin-email:latest-stack")
    args = parser.parse_args()
    os.umask(0o077)
    root = Path(args.work_dir)
    assert root.is_absolute() and not root.exists(), "use a NEW dedicated evidence directory"
    root.mkdir(mode=0o700)
    app = root / "source"
    for name in ("data/redis", "data/avatars", "data/attachments", "mail/example.test/user/cur",
                 "mail/example.test/user/new", "mail/example.test/user/tmp", "dkim", "nginx", "certs",
                 "backups/work", "backups/artifacts", "backups/restore", "acme-webroot"):
        (app / name).mkdir(parents=True, exist_ok=True)
    source_dir = Path(__file__).resolve().parent
    tools = root / "tools"
    tools.mkdir()
    for name in ("lanqin-backup.py", "lanqin-backup.sh", "lanqin-restore.sh"):
        shutil.copyfile(source_dir / name, tools / name)
    env_text = "LANQIN_NOTIFICATION_SECRET_KEY=synthetic-notification-secret\nLANQIN_BACKUP_SECRET_KEY=synthetic-backup-secret\n"
    (app / ".env").write_text(env_text)
    (app / "data/avatars/user").write_bytes(b"avatar fixture")
    (app / "data/attachments/fixture").write_bytes(b"attachment fixture")
    (app / "mail/example.test/user/cur/message:2,S").write_bytes(b"Subject: fixture\r\n\r\nmail fixture")
    (app / "dkim/example.test.key").write_text("synthetic dkim")
    (app / "nginx/site-80.conf").write_text("server {listen 80; location / {return 200 'fixture';}}\n")
    (app / "nginx/ssl-443.conf").write_text("server {listen 443 ssl; ssl_certificate /certs/fullchain.pem; ssl_certificate_key /certs/privkey.pem; location / {return 200 'fixture';}}\n")
    certstore = root / "external-certstore"
    certstore.mkdir()
    command(["docker", "run", "--rm", "--network", "none", "--mount",
             f"type=bind,source={certstore},target=/cert", "--entrypoint", "/bin/sh", args.image,
             "-c", "openssl req -x509 -newkey rsa:2048 -nodes -keyout /cert/key.pem -out /cert/cert.pem -days 1 -subj /CN=example.test >/dev/null 2>&1"])
    (app / "certs/fullchain.pem").symlink_to(certstore / "cert.pem")
    (app / "certs/privkey.pem").symlink_to(certstore / "key.pem")
    for name in ("work", "artifacts", "restore"):
        (app / "backups" / name / "must-not-be-backed-up").write_bytes(b"native artifact")
    (app / "data/lanqin.db.before-116").write_bytes(b"historical rollback must not recurse")
    external_file = root / "external-runtime.conf"
    external_file.write_text("external dependency fixture")
    name = "fix117-backup-fixture-" + str(os.getpid())
    service = {"image": args.image, "network_mode": "none", "entrypoint": ["/bin/sh", "-c"],
               "command": ["exec redis-server /etc/redis-lanqin.conf"], "env_file": ".env",
               "volumes": ["./data:/data", "./mail:/var/mail/vhosts", "./dkim:/var/lib/rspamd/dkim",
                           "./nginx/site-80.conf:/etc/nginx/conf.d/default.conf:ro",
                           "./nginx/ssl-443.conf:/etc/nginx/conf.d/ssl-443.conf:ro",
                           "./certs:/certs:ro", "./acme-webroot:/srv/acme:ro",
                           f"{external_file}:/runtime.conf:ro"]}
    # JSON is valid YAML; avoid requiring PyYAML on the recovery host.
    (app / "docker-compose.yml").write_text(json.dumps({"services": {"lanqin-email": service}}))
    (app / "docker-compose.override.yml").write_text(json.dumps({"services": {"lanqin-email": {
        "volumes": [f"./backups/{part}:/backups/{part}" for part in ("work", "artifacts", "restore")]}}}))
    command(["docker", "run", "--rm", "--network", "none", "--mount",
             f"type=bind,source={app / 'data'},target=/data", "--entrypoint", "/bin/sh", args.image,
             "-c", "sqlite3 /data/lanqin.db 'CREATE TABLE fixture(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO fixture VALUES(1,char(112,114,101,115,101,114,118,101,100));'"])
    # Leave committed rows in WAL with no live SQLite connection: reproduces
    # production's stopped DB. The snapshot worker sees /data read-only and must
    # rebuild SHM only in its private writable copy.
    command([sys.executable, "-c", "import os,sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('PRAGMA journal_mode=WAL'); c.execute('PRAGMA wal_autocheckpoint=0'); c.execute('CREATE TABLE wal_fixture(value TEXT)'); c.execute(\"INSERT INTO wal_fixture VALUES('wal-preserved')\"); c.commit(); os._exit(0)", str(app / "data/lanqin.db")])
    assert (app / "data/lanqin.db-wal").is_file()
    compose = ["docker", "compose", "--project-name", name, "--project-directory", str(app),
               "-f", str(app / "docker-compose.yml"), "-f", str(app / "docker-compose.override.yml")]
    cid = ""
    restored_cid = ""
    try:
        command(compose + ["up", "-d", "--pull", "never"])
        cid = command(compose + ["ps", "-q", "lanqin-email"])
        for _ in range(100):
            try:
                if command(["docker", "exec", cid, "redis-cli", "ping"]) == "PONG":
                    break
            except RuntimeError:
                pass
            time.sleep(.1)
        assert command(["docker", "exec", cid, "redis-cli", "set", "audit:recovery", "preserved"]) == "OK"
        command(["docker", "exec", cid, "redis-cli", "-n", "3", "hset", "audit:hash", "field", "preserved"])
        env = dict(os.environ, APP_DIR=str(app), BACKUP_DIR=str(root / "sets"), CONTAINER=cid)
        print(command(["bash", str(tools / "lanqin-backup.sh")], env=env))
        sets = [p for p in (root / "sets").iterdir() if p.is_dir()]
        assert len(sets) == 1
        backup = sets[0]
        print(command(["python3", str(tools / "lanqin-backup.py"), "verify", "--backup-dir", str(backup)]))
        # Use cwd so sha256sum resolves strict relative component names.
        hashes = subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=backup, text=True, capture_output=True)
        assert hashes.returncode == 0, hashes.stderr
        print(hashes.stdout.strip())
        assert backup.stat().st_mode & 0o777 == 0o700
        assert all(p.stat().st_mode & 0o777 == 0o600 for p in backup.iterdir())
        with tarfile.open(backup / "runtime.tar.gz") as tar:
            members = tar.getmembers()
            assert all(m.isfile() or m.isdir() for m in members), "cert symlinks should be materialized"
            assert not any("must-not-be-backed-up" in m.name or "before-116" in m.name for m in members)
        image_identity_cases(root, backup, tools, args.image)
        print("PASS private permissions, symlink materialization, no native artifact recursion or historical DB")
        restored = root / "restored-on-new-host"
        print(command(["bash", str(backup / "lanqin-restore.sh"), "--backup-dir", str(backup), "--app-dir", str(restored)]))
        for path in ("nginx/site-80.conf", "nginx/ssl-443.conf", "certs/fullchain.pem", "certs/privkey.pem",
                     "docker-compose.override.yml", "data/avatars/user", "data/attachments/fixture", "dkim/example.test.key"):
            assert (restored / path).is_file(), path
        assert (restored / ".env").read_text() == env_text
        for part in ("work", "artifacts", "restore"):
            assert not list((restored / "backups" / part).iterdir())
        restored_compose = ["docker", "compose", "--project-name", name + "-restored", "-f", str(restored / "docker-compose.restore.json")]
        # Actually create/start all restored mounts; config -q alone cannot catch not-directory.
        command(restored_compose + ["up", "-d", "--pull", "never"])
        restored_cid = command(restored_compose + ["ps", "-q", "lanqin-email"])
        for _ in range(100):
            try:
                if command(["docker", "exec", restored_cid, "redis-cli", "get", "audit:recovery"]) == "preserved":
                    break
            except RuntimeError:
                pass
            time.sleep(.1)
        assert command(["docker", "exec", restored_cid, "redis-cli", "get", "audit:recovery"]) == "preserved"
        assert command(["docker", "exec", restored_cid, "redis-cli", "-n", "3", "hget", "audit:hash", "field"]) == "preserved"
        assert command(["docker", "exec", restored_cid, "nginx", "-t"]) == ""
        assert command(["docker", "exec", restored_cid, "sqlite3", "/data/lanqin.db", "SELECT value FROM fixture;"]) == "preserved"
        assert command(["docker", "exec", restored_cid, "sqlite3", "/data/lanqin.db", "SELECT value FROM wal_fixture;"]) == "wal-preserved"
        print("PASS real bind mounts, nginx TLS config, avatar/key files, SQLite WAL row, Redis string+hash (DB0/DB3) after normal AOF=yes startup")
        command(["docker", "rm", "-f", restored_cid])
        restored_cid = ""
        # Tampering is detected before APP_DIR gets created.
        tampered = root / "tampered"
        shutil.copytree(backup, tampered)
        with (tampered / "runtime.tar.gz").open("ab") as out:
            out.write(b"corrupt")
        rejected = root / "must-stay-absent"
        command(["bash", str(tools / "lanqin-restore.sh"), "--backup-dir", str(tampered), "--app-dir", str(rejected)], fail=True)
        assert not rejected.exists()
        # Rehashing does not make path traversal/link archives acceptable.
        malicious = root / "malicious"
        shutil.copytree(backup, malicious)
        for member_name, symlink in (("root/../../escape", False), ("root/data/escape", True)):
            with tarfile.open(malicious / "runtime.tar.gz", "w:gz") as tar:
                item = tarfile.TarInfo(member_name)
                if symlink:
                    item.type, item.linkname = tarfile.SYMTYPE, "/root"
                    tar.addfile(item)
                else:
                    item.size = 1
                    tar.addfile(item, io.BytesIO(b"x"))
            checksum_set(malicious)
            command(["bash", str(tools / "lanqin-restore.sh"), "--backup-dir", str(malicious), "--app-dir", str(rejected)], fail=True)
            assert not rejected.exists()
        command(["bash", str(tools / "lanqin-restore.sh"), "--backup-dir", str(backup), "--app-dir", str(app)], fail=True)
        # A changed root key in .env must not silently be paired with an old live DB.
        with (app / ".env").open("a") as stream:
            stream.write("LANQIN_NOTIFICATION_SECRET_KEY=changed-fixture-key\n")
        command(["bash", str(tools / "lanqin-backup.sh")], env=env, fail=True,
                expected="compose environment differs")
        (app / ".env").write_text(env_text)
        # A missing bind file must fail discovery before the live container is stopped.
        external_file.unlink()
        command(["bash", str(tools / "lanqin-backup.sh")], env=env, fail=True)
        assert command(["docker", "inspect", cid, "--format", "{{.State.Running}}"] ) == "true"
        external_file.write_text("external dependency fixture")
        # A newly written inode at the same host path is NOT the mounted file.
        command(["bash", str(tools / "lanqin-backup.sh")], env=env, fail=True,
                expected="single-file bind inode differs")
        command(compose + ["up", "-d", "--force-recreate", "--pull", "never"])
        cid = command(compose + ["ps", "-q", "lanqin-email"])
        env["CONTAINER"] = cid
        # Worker failure must resume the source and publish no incomplete date-named set.
        time.sleep(1.1)
        broken = root / "broken-tools"
        shutil.copytree(tools, broken)
        helper = broken / "lanqin-backup.py"
        helper.write_text(helper.read_text().replace("SNAPSHOT_WORKER = r'''set -eu", "SNAPSHOT_WORKER = r'''exit 73\nset -eu", 1))
        command(["bash", str(broken / "lanqin-backup.sh")], env=env, fail=True)
        assert command(["docker", "inspect", cid, "--format", "{{.State.Running}}"] ) == "true"
        assert len([p for p in (root / "sets").iterdir() if p.is_dir()]) == 1
        command(["docker", "exec", cid, "/bin/sh", "-c",
                 "mkdir -p /var/spool/postfix/deferred; printf queued-fixture > /var/spool/postfix/deferred/synthetic"])
        time.sleep(1.1)
        command(["bash", str(tools / "lanqin-backup.sh")], env=env, fail=True,
                expected="Postfix has queued mail")
        assert command(["docker", "inspect", cid, "--format", "{{.State.Running}}"] ) == "true"
        assert len([p for p in (root / "sets").iterdir() if p.is_dir()]) == 1
        print("PASS missing bind fail-closed; failed snapshot/queued mail resumes source; no incomplete recovery set")
        print(f"ALL PASS; evidence: {root}; valid recovery set: {backup}")
    finally:
        for container in (restored_cid, cid):
            if container:
                command(["docker", "rm", "-f", container])


if __name__ == "__main__":
    main()
