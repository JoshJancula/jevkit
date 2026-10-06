import importlib.util
import unittest
from pathlib import Path


spec = importlib.util.spec_from_file_location(
    "release_notes", Path(__file__).with_name("release-notes.py")
)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ReleaseNotesTest(unittest.TestCase):
    def test_curated_entry_preserves_sections_and_stops_at_next_release(self):
        changelog = "# Changelog\n## [Unreleased]\nDraft\n## [0.2.0](url) - 2026-10-01\n\n### Added\n- New feature\n\n## [0.1.0](url)\nOld notes\n"
        self.assertEqual(
            module.release_notes(changelog, "v0.2.0"),
            "### Added\n- New feature\n",
        )

    def test_release_please_patch_and_minor_headers(self):
        changelog = "# Changelog\n### [0.1.1](url) (2026-10-01)\n\n### Fixed\n- A fix\n\n## 0.1.0 (2026-09-30)\nInitial release\n"
        self.assertEqual(module.release_notes(changelog, "v0.1.1"), "### Fixed\n- A fix\n")
        self.assertEqual(module.release_notes(changelog, "v0.1.0"), "Initial release\n")

    def test_prerelease(self):
        self.assertEqual(
            module.release_notes("## [1.0.0-rc.1](url)\nPreview", "v1.0.0-rc.1"),
            "Preview\n",
        )

    def test_missing_empty_and_duplicate_entries_fail(self):
        for changelog in ("## 0.2.0\nOther", "## 0.1.0\n", "## 0.1.0\nOne\n## 0.1.0\nTwo"):
            with self.subTest(changelog=changelog), self.assertRaises(ValueError):
                module.release_notes(changelog, "v0.1.0")

    def test_invalid_tag_fails(self):
        with self.assertRaises(ValueError):
            module.release_notes("## 0.1.0\nNotes", "main")


if __name__ == "__main__":
    unittest.main()
