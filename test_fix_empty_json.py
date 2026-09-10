import unittest
from fix_empty_json import candidate_query, repair_command


class FixEmptyJSONTests(unittest.TestCase):
    def test_selection_is_parameterized_and_only_empty_features(self):
        path = r"M:\Music\quote'_% album.flac"
        query, args = candidate_query(path, [89476, 89480])
        self.assertIn("features = '{}'::jsonb", query)
        self.assertIn("filepath = %s", query)
        self.assertNotIn(path, query)
        self.assertEqual(args, [path, [89476, 89480]])
        self.assertNotIn("predictions =", query)

    def test_each_command_targets_an_id_not_a_whole_album_force(self):
        row = (89476, r"M:\Music\album.flac", 1, "Remember", "hash")
        command = repair_command("single-orchestrator.exe", row)
        self.assertEqual(command[-2:], ["-fix-record", "89476"])
        self.assertNotIn("-force", command)
        self.assertIn(row[1], command)


if __name__ == "__main__":
    unittest.main()
