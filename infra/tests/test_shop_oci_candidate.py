from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
CANDIDATE = ROOT / "infra" / "oci" / "shop-candidate"


class ShopOciCandidateBoundaryTests(unittest.TestCase):
    def test_candidate_compose_is_private_and_uses_its_own_data_volume(self):
        source = (CANDIDATE / "compose.yaml").read_text(encoding="utf-8").lower()
        self.assertIn("127.0.0.1:${shop_candidate_port", source)
        self.assertIn("internal: true", source)
        self.assertIn("business-suite-shop-candidate-data", source)
        self.assertIn("shop_candidate_image", source)
        self.assertIn("shop_candidate_revision", source)
        self.assertIn("com.business-suite.shop-candidate.owner", source)
        for forbidden in ("caddy", "edge:", "web-data", "station-data", "/srv/app/.data", "portfolio.sqlite"):
            self.assertNotIn(forbidden, source)

    def test_release_script_is_digest_pinned_and_candidate_scoped(self):
        source = (CANDIDATE / "deploy-release.sh").read_text(encoding="utf-8").lower()
        self.assertIn("/srv/business-suite-shop-candidate", source)
        self.assertIn("sha256:", source)
        self.assertIn("docker image inspect", source)
        self.assertIn("org.opencontainers.image.revision", source)
        self.assertIn("docker compose", source)
        self.assertIn("api/health", source)
        self.assertIn("revision", source)
        self.assertIn("flock", source)
        for forbidden in (
            "/srv/business-suite/current",
            "portfolio.sqlite",
            "compose-public-sites",
            "service=edge",
            "down -v",
            "--volumes",
        ):
            self.assertNotIn(forbidden, source)

    def test_ssh_gateway_accepts_only_one_release_command_shape(self):
        source = (CANDIDATE / "ssh-gateway.sh").read_text(encoding="utf-8")
        self.assertIn("deploy\\ ([0-9a-f]{40})\\ (sha256:[0-9a-f]{64})", source)
        self.assertIn("deploy-release.sh", source)
        self.assertNotIn("eval", source)
        self.assertNotIn("bash -c", source)

    def test_candidate_lifecycle_smoke_uses_synthetic_checkout_and_restore(self):
        source = (CANDIDATE / "smoke_test.py").read_text(encoding="utf-8").lower()
        for required in (
            "suite_demo_space",
            "admin@suite.test",
            "checkout",
            "force-recreate",
            "shop-candidate.sqlite",
            "restore",
            "127.0.0.1",
        ):
            self.assertIn(required, source)
        self.assertNotIn("portfolio.sqlite", source)

    def test_main_workflow_only_publishes_candidate_index_and_does_not_deploy(self):
        source = (ROOT / ".github" / "workflows" / "shop.yml").read_text(encoding="utf-8")
        self.assertIn("publish-candidate-index:", source)
        self.assertIn("-private-candidate", source)
        self.assertIn("packages: write", source)
        self.assertNotIn("deploy-private-candidate:", source)
        self.assertNotIn("ssh-gateway.sh", source)


if __name__ == "__main__":
    unittest.main()
