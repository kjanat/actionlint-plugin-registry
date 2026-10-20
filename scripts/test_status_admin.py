import unittest

from status_admin import actor_identity


class StatusAdminTest(unittest.TestCase):
    def setUp(self):
        self.env = {
            "GITHUB_ACTOR": "maintainer",
            "GITHUB_ACTOR_ID": "123",
            "GITHUB_REPOSITORY": "owner/registry",
            "GITHUB_WORKFLOW_REF": "owner/registry/.github/workflows/status.yml@refs/heads/master",
            "GITHUB_EVENT_NAME": "workflow_dispatch",
            "GITHUB_REF": "refs/heads/master",
            "GITHUB_TRIGGERING_ACTOR": "maintainer",
            "GITHUB_RUN_ID": "10",
            "GITHUB_RUN_ATTEMPT": "1",
        }
        self.permission = {
            "permission": "admin",
            "role_name": "admin",
            "user": {"id": 123, "login": "maintainer"},
        }

    def test_admin_and_maintain(self):
        for role, permission in [("admin", "admin"), ("maintain", "write")]:
            with self.subTest(role=role):
                self.permission.update(role_name=role, permission=permission)
                actor = actor_identity(self.env, self.permission)
                self.assertEqual(actor["id"], "123")
                self.assertEqual(actor["run_id"], "10:1")

    def test_write_permission_is_insufficient(self):
        self.permission.update(permission="write", role_name="write")
        with self.assertRaises(ValueError):
            actor_identity(self.env, self.permission)

    def test_untrusted_context(self):
        for key, value in [
            ("GITHUB_REF", "refs/heads/feature"),
            ("GITHUB_EVENT_NAME", "pull_request"),
            ("GITHUB_TRIGGERING_ACTOR", "someone-else"),
            ("GITHUB_ACTOR_ID", "999"),
            (
                "GITHUB_WORKFLOW_REF",
                "evil/registry/.github/workflows/status.yml@refs/heads/master",
            ),
            ("GITHUB_RUN_ATTEMPT", "0"),
        ]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                actor_identity({**self.env, key: value}, self.permission)
