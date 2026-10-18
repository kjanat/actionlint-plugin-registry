import re
import shutil
import subprocess
import unittest
from pathlib import Path


class ValidationLoggingTests(unittest.TestCase):
    def test_resume_after_success_and_failure_without_interpreting_guest_text(self):
        wrapper = (Path(__file__).parent / "validate.sh").resolve().as_posix()
        bash = shutil.which("bash")
        self.assertIsNotNone(bash)
        tokens = []
        for status in (0, 7):
            with self.subTest(status=status):
                result = subprocess.run(
                    [
                        bash,
                        wrapper,
                        "bash",
                        "-c",
                        "printf '%s\\n' '::warning::guest text'; exit \"$1\"",
                        "test",
                        str(status),
                    ],
                    capture_output=True,
                    text=True,
                    timeout=15,
                    check=False,
                )
                self.assertEqual(result.returncode, status, result.stderr)
                lines = result.stdout.splitlines()
                self.assertEqual(len(lines), 3)
                match = re.fullmatch(r"::stop-commands::([0-9a-f]{64})", lines[0])
                self.assertIsNotNone(match)
                token = match.group(1)
                tokens.append(token)
                self.assertEqual(lines[1], "::warning::guest text")
                self.assertEqual(lines[2], f"::{token}::")
        self.assertEqual(len(tokens), 2)
        self.assertNotEqual(tokens[0], tokens[1])


if __name__ == "__main__":
    unittest.main()
