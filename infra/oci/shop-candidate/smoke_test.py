#!/usr/bin/env python3
"""Exercise a locally built Shop image with synthetic data and isolated volumes."""

from __future__ import annotations

import http.cookiejar
import json
import os
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
COMPOSE_FILE = ROOT / "infra" / "oci" / "shop-candidate" / "compose.yaml"


def run(command: list[str], *, env: dict[str, str] | None = None) -> str:
    completed = subprocess.run(command, check=True, text=True, capture_output=True, env=env)
    return completed.stdout.strip()


def reserve_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class CandidateClient:
    def __init__(self, base: str) -> None:
        self.base = base
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookies))

    def request(self, method: str, path: str, payload: dict | None = None, extra_headers: dict[str, str] | None = None) -> tuple[int, dict]:
        data = None if payload is None else json.dumps(payload).encode("utf-8")
        headers = {"Origin": self.base}
        if data is not None:
            headers["Content-Type"] = "application/json"
        if extra_headers:
            headers.update(extra_headers)
        request = urllib.request.Request(self.base + path, data=data, headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=5) as response:
                return response.status, json.load(response)
        except urllib.error.HTTPError as response:
            try:
                body = json.load(response)
            except Exception:
                body = {}
            return response.code, body


def wait_for_revision(client: CandidateClient, revision: str) -> None:
    deadline = time.monotonic() + 180
    last_error: Exception | str = "not ready"
    while time.monotonic() < deadline:
        try:
            status, body = client.request("GET", "/api/health")
            actual = body.get("data", {}).get("revision")
            if status == 200 and actual == revision:
                return
            last_error = f"health={status}, revision={actual!r}"
        except Exception as error:
            last_error = error
        time.sleep(2)
    raise RuntimeError(f"candidate did not become healthy at {revision}: {last_error}")


def make_project_env(image: str, revision: str, port: int, volume: str, network: str, owner: str) -> dict[str, str]:
    env = os.environ.copy()
    env.update(
        SHOP_CANDIDATE_IMAGE=image,
        SHOP_CANDIDATE_REVISION=revision,
        SHOP_CANDIDATE_PORT=str(port),
        SHOP_CANDIDATE_VOLUME=volume,
        SHOP_CANDIDATE_NETWORK=network,
        SHOP_CANDIDATE_OWNER=owner,
    )
    return env


def compose(project: str, env: dict[str, str], *args: str) -> str:
    return run(["docker", "compose", "--project-name", project, "-f", str(COMPOSE_FILE), *args], env=env)


def exercise_order(image: str, revision: str, port: int, project: str, volume: str, network: str, owner: str) -> tuple[str, str]:
    env = make_project_env(image, revision, port, volume, network, owner)
    compose(project, env, "up", "-d", "--wait", "--wait-timeout", "180", "shop-candidate")
    base = f"http://127.0.0.1:{port}"
    client = CandidateClient(base)
    wait_for_revision(client, revision)

    unauthenticated_status, _ = client.request(
        "POST",
        "/api/stations/shop/business",
        {"action": "checkout", "variantId": "synthetic-unauthorized", "quantity": 1, "idempotencyKey": "candidate-unauth-001"},
    )
    if unauthenticated_status != 401:
        raise RuntimeError(f"unauthenticated Shop write returned {unauthenticated_status}")

    synthetic_space = secrets.token_hex(32)
    login_status, login_body = client.request(
        "POST",
        "/api/auth/login",
        {"email": "admin@suite.test", "password": "SuiteDemo!2026", "site": "shop"},
        {"Cookie": f"suite_demo_space={synthetic_space}"},
    )
    if login_status != 200 or login_body.get("data", {}).get("signedIn") is not True:
        raise RuntimeError(f"synthetic demo login failed: {login_status} {login_body}")

    catalog_status, catalog_body = client.request("GET", "/api/stations/shop/data?view=front&dataset=variants")
    variants = catalog_body.get("data", {}).get("variants", [])
    if catalog_status != 200 or not variants:
        raise RuntimeError(f"synthetic Shop catalog is unavailable: {catalog_status}")
    variant = next((item for item in variants if int(item.get("availableQuantity", 0)) > 0), None)
    if variant is None:
        raise RuntimeError("synthetic Shop fixtures contain no available product variant")

    checkout_status, checkout_body = client.request(
        "POST",
        "/api/stations/shop/business",
        {"action": "checkout", "variantId": variant["id"], "quantity": 1, "idempotencyKey": "candidate-lifecycle-order-0001"},
    )
    order_id = checkout_body.get("data", {}).get("orderId")
    if checkout_status != 200 or not order_id:
        raise RuntimeError(f"synthetic checkout failed: {checkout_status} {checkout_body}")

    compose(project, env, "up", "-d", "--force-recreate", "--wait", "--wait-timeout", "180", "shop-candidate")
    wait_for_revision(client, revision)
    orders_status, orders_body = client.request("GET", "/api/stations/shop/data?view=front&dataset=orders")
    orders = orders_body.get("data", {}).get("orders", [])
    if orders_status != 200 or not any(order.get("id") == order_id for order in orders):
        raise RuntimeError("synthetic order did not survive candidate container replacement")
    return order_id, synthetic_space


def verify_restored_order(image: str, revision: str, port: int, project: str, volume: str, network: str, owner: str, order_id: str, synthetic_space: str) -> None:
    env = make_project_env(image, revision, port, volume, network, owner)
    compose(project, env, "up", "-d", "--wait", "--wait-timeout", "180", "shop-candidate")
    client = CandidateClient(f"http://127.0.0.1:{port}")
    wait_for_revision(client, revision)
    status, body = client.request(
        "POST",
        "/api/auth/login",
        {"email": "admin@suite.test", "password": "SuiteDemo!2026", "site": "shop"},
        {"Cookie": f"suite_demo_space={synthetic_space}"},
    )
    if status != 200 or body.get("data", {}).get("signedIn") is not True:
        raise RuntimeError(f"synthetic restore login failed: {status} {body}")
    status, body = client.request("GET", "/api/stations/shop/data?view=front&dataset=orders")
    if status != 200 or not any(order.get("id") == order_id for order in body.get("data", {}).get("orders", [])):
        raise RuntimeError("synthetic order was not present after isolated-volume restore")


def main() -> int:
    image = os.environ.get("SHOP_CANDIDATE_TEST_IMAGE", "")
    revision = os.environ.get("GITHUB_SHA", "")
    if not image or not revision or len(revision) != 40:
        print("SHOP_CANDIDATE_TEST_IMAGE and a 40-character GITHUB_SHA are required", file=sys.stderr)
        return 2

    suffix = secrets.token_hex(8)
    if not all(ch in "0123456789abcdef" for ch in suffix):
        raise RuntimeError("invalid ephemeral project suffix")
    base_project = f"shop-candidate-ci-{suffix}"
    restore_project = f"{base_project}-restore"
    base_volume = f"business-suite-shop-candidate-ci-{suffix}"
    restore_volume = f"business-suite-shop-candidate-restore-ci-{suffix}"
    base_port, restore_port = reserve_port(), reserve_port()
    while restore_port == base_port:
        restore_port = reserve_port()
    base_network = f"shop-candidate-ci-net-{suffix}"
    restore_network = f"shop-candidate-restore-ci-net-{suffix}"
    base_owner = f"shop-candidate-ci-{suffix}"
    restore_owner = f"shop-candidate-restore-ci-{suffix}"
    base_env = make_project_env(image, revision, base_port, base_volume, base_network, base_owner)
    restore_env = make_project_env(image, revision, restore_port, restore_volume, restore_network, restore_owner)
    backup_parent = Path(tempfile.mkdtemp(prefix="shop-candidate-restore-"))
    backup_dir = backup_parent / "candidate-data"
    backup_dir.mkdir(mode=0o700)
    try:
        for project, env, volume, network in (
            (base_project, base_env, base_volume, base_network),
            (restore_project, restore_env, restore_volume, restore_network),
        ):
            existing = subprocess.run(
                ["docker", "compose", "--project-name", project, "-f", str(COMPOSE_FILE), "ps", "--all", "-q"],
                env=env, capture_output=True, text=True, check=True,
            ).stdout.strip()
            if existing:
                raise RuntimeError(f"refusing to reuse existing Compose project {project}")
            for kind, name in (("volume", volume), ("network", network)):
                if subprocess.run(["docker", kind, "inspect", name], capture_output=True).returncode == 0:
                    raise RuntimeError(f"refusing to reuse existing ephemeral {kind} {name}")

        order_id, synthetic_space = exercise_order(image, revision, base_port, base_project, base_volume, base_network, base_owner)
        container_id = compose(base_project, base_env, "ps", "-q", "shop-candidate")
        if not container_id:
            raise RuntimeError("candidate container ID is missing before snapshot")
        compose(base_project, base_env, "stop", "shop-candidate")
        run(["docker", "cp", f"{container_id}:/srv/station/data/.", str(backup_dir)])
        if not (backup_dir / "shop-candidate.sqlite").is_file():
            raise RuntimeError("candidate SQLite snapshot is missing")

        run([
            "docker", "volume", "create",
            "--label", f"com.business-suite.shop-candidate.owner={restore_owner}",
            "--label", f"com.docker.compose.project={restore_project}",
            "--label", "com.docker.compose.volume=candidate-data",
            restore_volume,
        ])
        run(
            [
                "docker", "run", "--rm", "--network", "none", "--user", "0:0",
                "--mount", f"type=volume,src={restore_volume},dst=/data",
                "--mount", f"type=bind,src={backup_dir},dst=/backup,readonly",
                "--entrypoint", "/bin/sh", image, "-ec",
                "find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; cp -a /backup/. /data/; chown -R 10001:10001 /data; chmod -R u+rwX,go-rwx /data",
            ]
        )
        verify_restored_order(image, revision, restore_port, restore_project, restore_volume, restore_network, restore_owner, order_id, synthetic_space)
        print("PASS: private Compose lifecycle, synthetic checkout, replacement persistence, and isolated-volume restore")
        return 0
    finally:
        for project, env in ((restore_project, restore_env), (base_project, base_env)):
            try:
                compose(project, env, "down", "--remove-orphans", "--timeout", "10")
            except Exception as error:
                print(f"cleanup warning for {project}: {error}", file=sys.stderr)
        for volume, owner in ((restore_volume, restore_owner), (base_volume, base_owner)):
            try:
                result = subprocess.run(
                    ["docker", "volume", "inspect", volume, "--format", '{{ index .Labels "com.business-suite.shop-candidate.owner" }}'],
                    capture_output=True, text=True,
                )
                if result.returncode == 0 and result.stdout.strip() == owner:
                    run(["docker", "volume", "rm", volume])
                elif result.returncode == 0:
                    print(f"cleanup skipped volume without matching ownership label: {volume}", file=sys.stderr)
            except Exception as error:
                print(f"cleanup warning for ephemeral volume {volume}: {error}", file=sys.stderr)
        shutil.rmtree(backup_parent, ignore_errors=True)


if __name__ == "__main__":
    raise SystemExit(main())
