from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DEPLOY = ROOT / "infra/oci/shop-candidate/deploy-release.sh"
REPOSITORY = "ghcr.io/davis1233798/business-suite-shop-station"


@unittest.skipUnless(os.name == "posix", "release script rollback harness requires POSIX shell paths")
class ShopReleaseRollbackTests(unittest.TestCase):
    def test_failed_candidate_restores_snapshot_before_previous_image_health(self) -> None:
        with tempfile.TemporaryDirectory(prefix="shop-release-rollback-") as temp:
            base = Path(temp)
            root, shared, bin_dir = base / "candidate", base / "shared", base / "bin"
            volume_dir = base / "candidate-volume"
            shared.mkdir(); bin_dir.mkdir(); volume_dir.mkdir()
            old_database = b"pre-upgrade-synthetic-database"
            database = volume_dir / "shop-candidate.sqlite"
            database.write_bytes(old_database)
            previous_sha, new_sha = "1" * 40, "2" * 40
            previous_digest, new_digest = "a" * 64, "b" * 64
            test_volume = "synthetic-shop-candidate-volume"
            previous = root / "releases" / previous_sha
            previous.mkdir(parents=True)
            (previous / "COMMIT").write_text(previous_sha)
            (previous / "IMAGE").write_text(f"{REPOSITORY}@sha256:{previous_digest}")
            (root / "current").symlink_to(previous)
            (shared / "compose.yaml").write_text("services: {}\n")

            script_text = DEPLOY.read_text()
            script_text = script_text.replace(f"repository={REPOSITORY}", f"repository={REPOSITORY}")
            script_text = script_text.replace("root=/srv/business-suite-shop-candidate", f"root={root}")
            script_text = script_text.replace("shared=/usr/local/lib/business-suite-shop-candidate", f"shared={shared}")
            script_text = script_text.replace("volume=business-suite-shop-candidate-data", f"volume={test_volume}")
            script = base / "deploy-release.sh"
            script.write_text(script_text); script.chmod(0o700)

            docker = bin_dir / "docker"
            docker.write_text(r'''#!/usr/bin/env python3
import os, pathlib, shutil, sys
args = sys.argv[1:]
volume = pathlib.Path(os.environ["MOCK_VOLUME_PATH"])
database = volume / "shop-candidate.sqlite"
def log(event):
    with open(os.environ["MOCK_LOG"], "a") as stream: stream.write(event + "\n")
if args[:1] == ["pull"]:
    pass
elif args[:2] == ["image", "inspect"]:
    image, fmt = args[2], args[4]
    if ".Architecture" in fmt: print("amd64")
    elif "org.opencontainers.image.revision" in fmt:
        print(os.environ["PREVIOUS_SHA"] if "sha256:" + "a" * 64 in image else os.environ["NEW_LABEL_SHA"])
    else: raise SystemExit(3)
elif args[:1] == ["compose"]:
    project_dir = pathlib.Path(args[args.index("--project-directory") + 1])
    command = args[args.index("-f") + 2:]
    sha = (project_dir / "COMMIT").read_text().strip()
    if command[:2] == ["ps", "-q"]:
        print("previous-container" if sha == os.environ["PREVIOUS_SHA"] else "new-container")
    elif command and command[0] == "up" and sha == os.environ["NEW_SHA"]:
        database.write_bytes(b"new-release-migration-failed")
        log("new-start-failed")
        raise SystemExit(1)
    elif command and command[0] == "up" and sha == os.environ["PREVIOUS_SHA"]:
        if not database.is_file() or database.read_bytes() != b"pre-upgrade-synthetic-database":
            log("previous-start-wrong-database")
            raise SystemExit(20)
        pathlib.Path(os.environ["OLD_IMAGE_STARTED"]).write_text(sha)
        log("previous-started-with-restored-database")
elif args[:2] == ["volume", "inspect"]:
    raise SystemExit(1)
elif args[:1] == ["cp"]:
    if args[1] != "previous-container:/srv/station/data/.":
        raise SystemExit("snapshot source was not the prior candidate container")
    destination = pathlib.Path(args[2])
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copy2(database, destination / "shop-candidate.sqlite")
elif args[:1] == ["run"]:
    mounts = [arg for arg in args if arg.startswith("type=")]
    volume_mount = next(arg for arg in mounts if ",dst=/data" in arg)
    expected_mount = f"type=volume,src={os.environ['MOCK_VOLUME_NAME']},dst=/data"
    if volume_mount != expected_mount:
        raise SystemExit("rollback attempted to restore a volume outside the candidate volume")
    backup_mount = next(arg for arg in mounts if ",dst=/backup," in arg)
    backup = pathlib.Path(backup_mount.split(",src=", 1)[1].split(",dst=", 1)[0])
    if not backup.resolve().is_relative_to(pathlib.Path(os.environ["MOCK_BACKUP_ROOT"]).resolve()):
        raise SystemExit("snapshot source escaped the candidate backup root")
    if not any(arg == f"{os.environ['PREVIOUS_IMAGE']}" for arg in args):
        raise SystemExit("snapshot restore did not use the prior image")
    shutil.copy2(backup / "shop-candidate.sqlite", database)
    log("snapshot-restored-to-candidate-volume")
else:
    raise SystemExit("unexpected docker command: " + repr(args))
''')
            docker.chmod(0o700)
            curl = bin_dir / "curl"
            curl.write_text("#!/usr/bin/env python3\nimport json,os,pathlib,sys\nstarted=pathlib.Path(os.environ['OLD_IMAGE_STARTED'])\nif not started.exists(): raise SystemExit(22)\nprint(json.dumps({'data': {'revision': started.read_text()}}))\n")
            curl.chmod(0o700)

            env = os.environ.copy()
            env.update(
                PATH=f"{bin_dir}{os.pathsep}{env['PATH']}",
                PREVIOUS_SHA=previous_sha, NEW_SHA=new_sha, NEW_LABEL_SHA=new_sha,
                MOCK_LOG=str(base / "mock.log"), MOCK_VOLUME_PATH=str(volume_dir),
                OLD_IMAGE_STARTED=str(base / "old-image-started"),
                MOCK_VOLUME_NAME=test_volume, MOCK_BACKUP_ROOT=str(root / "backups"),
                PREVIOUS_IMAGE=f"{REPOSITORY}@sha256:{previous_digest}",
            )
            result = subprocess.run(["bash", str(script), new_sha, f"sha256:{new_digest}"], env=env, text=True, capture_output=True)

            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual((root / "current").resolve(), previous.resolve())
            self.assertEqual((previous / "COMMIT").read_text(), previous_sha)
            self.assertEqual(database.read_bytes(), old_database)
            events = (base / "mock.log").read_text().splitlines()
            self.assertLess(events.index("snapshot-restored-to-candidate-volume"), events.index("previous-started-with-restored-database"))
            self.assertIn("new-start-failed", events)
            self.assertEqual((base / "old-image-started").read_text(), previous_sha)
            snapshots = list((root / "backups").glob("*/shop-candidate.sqlite"))
            self.assertEqual(len(snapshots), 1)
            self.assertEqual(snapshots[0].read_bytes(), old_database)
            self.assertIn("restoring only the prior candidate release", result.stderr)

    def test_image_revision_label_must_match_requested_commit_before_change(self) -> None:
        with tempfile.TemporaryDirectory(prefix="shop-release-label-") as temp:
            base = Path(temp)
            root, shared, bin_dir = base / "candidate", base / "shared", base / "bin"
            shared.mkdir(); bin_dir.mkdir()
            sha = "3" * 40
            (shared / "compose.yaml").write_text("services: {}\n")
            script_text = DEPLOY.read_text()
            script_text = script_text.replace("root=/srv/business-suite-shop-candidate", f"root={root}")
            script_text = script_text.replace("shared=/usr/local/lib/business-suite-shop-candidate", f"shared={shared}")
            script = base / "deploy-release.sh"
            script.write_text(script_text); script.chmod(0o700)
            docker = bin_dir / "docker"
            docker.write_text('''#!/usr/bin/env python3
import os,sys
args=sys.argv[1:]
if args[:1] == ["pull"]:
    pass
elif args[:2] == ["image", "inspect"]:
    print("amd64" if ".Architecture" in args[4] else os.environ["WRONG_LABEL"])
else:
    raise SystemExit("unexpected command: " + repr(args))
''')
            docker.chmod(0o700)
            env = os.environ.copy()
            env.update(PATH=f"{bin_dir}{os.pathsep}{env['PATH']}", WRONG_LABEL="f" * 40)
            result = subprocess.run(["bash", str(script), sha, "sha256:" + "c" * 64], env=env, text=True, capture_output=True)
            self.assertEqual(result.returncode, 5, result.stdout + result.stderr)
            self.assertIn("revision label does not match", result.stderr)
            self.assertFalse((root / "releases" / sha).exists())

    def test_failed_first_release_requires_matching_marker_and_preserves_volume_for_retry(self) -> None:
        with tempfile.TemporaryDirectory(prefix="shop-release-first-retry-") as temp:
            base = Path(temp)
            root, shared, bin_dir = base / "candidate", base / "shared", base / "bin"
            volume = base / "candidate-volume"
            shared.mkdir(); bin_dir.mkdir()
            sha, alternate_sha = "4" * 40, "5" * 40
            digest = "d" * 64
            (shared / "compose.yaml").write_text("services: {}\n")
            script_text = DEPLOY.read_text()
            script_text = script_text.replace("root=/srv/business-suite-shop-candidate", f"root={root}")
            script_text = script_text.replace("shared=/usr/local/lib/business-suite-shop-candidate", f"shared={shared}")
            script = base / "deploy-release.sh"
            script.write_text(script_text); script.chmod(0o700)
            docker = bin_dir / "docker"
            docker.write_text(r'''#!/usr/bin/env python3
import os, pathlib, sys
args=sys.argv[1:]
volume=pathlib.Path(os.environ["MOCK_VOLUME_PATH"])
if args[:1] == ["pull"]:
    pass
elif args[:2] == ["image", "inspect"]:
    print("amd64" if ".Architecture" in args[4] else os.environ["IMAGE_LABEL"])
elif args[:2] == ["volume", "inspect"]:
    if args[2] != os.environ["EXPECTED_VOLUME_NAME"]:
        raise SystemExit("volume inspection targeted a non-candidate volume")
    raise SystemExit(0 if volume.exists() else 1)
elif args[:1] == ["compose"]:
    project_dir=pathlib.Path(args[args.index("--project-directory")+1])
    command=args[args.index("-f")+2:]
    if command and command[0] == "up":
        counter=pathlib.Path(os.environ["UP_COUNTER"])
        count=int(counter.read_text()) if counter.exists() else 0
        count += 1; counter.write_text(str(count))
        if count == 1:
            volume.mkdir(exist_ok=True)
            (volume / "partial-fixture.sqlite").write_bytes(b"retained-first-attempt-data")
            raise SystemExit(1)
elif args[:1] in (["stop"], ["rm"]):
    pass
else:
    raise SystemExit("unexpected docker command: " + repr(args))
''')
            docker.chmod(0o700)
            curl = bin_dir / "curl"
            curl.write_text("#!/usr/bin/env python3\nimport json,os\nprint(json.dumps({'data': {'revision': os.environ['HEALTH_SHA']}}))\n")
            curl.chmod(0o700)
            env = os.environ.copy()
            env.update(
                PATH=f"{bin_dir}{os.pathsep}{env['PATH']}", MOCK_VOLUME_PATH=str(volume),
                IMAGE_LABEL=sha, HEALTH_SHA=sha, UP_COUNTER=str(base / "up-count"),
                EXPECTED_VOLUME_NAME="business-suite-shop-candidate-data",
            )

            failed = subprocess.run(["bash", str(script), sha, f"sha256:{digest}"], env=env, text=True, capture_output=True)
            self.assertNotEqual(failed.returncode, 0)
            self.assertTrue((root / "INITIALIZING").is_file(), failed.stdout + failed.stderr)
            self.assertEqual((volume / "partial-fixture.sqlite").read_bytes(), b"retained-first-attempt-data")

            different_digest = subprocess.run(
                ["bash", str(script), sha, f"sha256:{'e' * 64}"], env=env, text=True, capture_output=True,
            )
            self.assertEqual(different_digest.returncode, 6, different_digest.stdout + different_digest.stderr)
            self.assertIn("different immutable candidate image", different_digest.stderr)
            self.assertEqual((volume / "partial-fixture.sqlite").read_bytes(), b"retained-first-attempt-data")
            self.assertEqual((base / "up-count").read_text(), "1")

            env["IMAGE_LABEL"] = alternate_sha
            mismatched = subprocess.run(["bash", str(script), alternate_sha, f"sha256:{'e' * 64}"], env=env, text=True, capture_output=True)
            self.assertEqual(mismatched.returncode, 8, mismatched.stdout + mismatched.stderr)
            self.assertIn("manual recovery is required", mismatched.stderr)
            self.assertEqual((root / "INITIALIZING").read_text().splitlines()[0], sha)
            self.assertEqual((base / "up-count").read_text(), "1")

            env["IMAGE_LABEL"] = sha
            retried = subprocess.run(["bash", str(script), sha, f"sha256:{digest}"], env=env, text=True, capture_output=True)
            self.assertEqual(retried.returncode, 0, retried.stdout + retried.stderr)
            self.assertEqual((root / "current").resolve(), (root / "releases" / sha).resolve())
            self.assertFalse((root / "INITIALIZING").exists())
            self.assertEqual((volume / "partial-fixture.sqlite").read_bytes(), b"retained-first-attempt-data")
            self.assertEqual((base / "up-count").read_text(), "2")


if __name__ == "__main__":
    unittest.main()
