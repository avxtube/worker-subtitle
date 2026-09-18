import unittest
from types import SimpleNamespace
from timestamp_fallback import needs_timestamp_fallback, normalize_segments, with_timestamp_fallback


class TimestampFallbackTests(unittest.TestCase):
    def test_only_untimed_nonempty_complete_output_uses_fallback(self):
        self.assertTrue(needs_timestamp_fallback("[S01]hello", [], 20, 4096))
        for text, segments, tokens in [("", [], 1), ("[0.00][S01][1.50]", [], 10),
                                        ("loop", [], 4096), ("valid", [{}], 20)]:
            self.assertFalse(needs_timestamp_fallback(text, segments, tokens, 4096))

    def test_preserves_moss_raw_and_marks_backend(self):
        timed = {"text": "hello", "segments": [{"start": 0, "end": 1, "text": "hello"}],
                 "generated_tokens": 0, "backend": "faster-whisper-small"}
        fallback = SimpleNamespace(transcribe=lambda _: timed)
        result = with_timestamp_fallback({"text": "[S01]hello", "segments": [], "generated_tokens": 20}, "audio", 4096, fallback)
        self.assertEqual(result["moss_text"], "[S01]hello")
        self.assertEqual(result["segments"], timed["segments"])

    def test_failure_is_not_silence(self):
        def fail(_):
            raise ValueError("no timed speech")
        result = with_timestamp_fallback({"text": "[S01]hello", "segments": [], "generated_tokens": 20}, "audio", 4096, SimpleNamespace(transcribe=fail))
        self.assertIn("Timestamp fallback failed", result["error"])
        self.assertFalse(result["retryable"])

    def test_bounds_and_unknown_speaker(self):
        result = normalize_segments([SimpleNamespace(start=.1, end=1.5, text=" hello ")], 2)
        self.assertEqual(result[0]["speaker"], "unknown")
        for segments in [[], [SimpleNamespace(start=-1, end=1, text="bad")],
                         [SimpleNamespace(start=0, end=20, text="bad")]]:
            with self.assertRaises(ValueError):
                normalize_segments(segments, 2)


if __name__ == "__main__":
    unittest.main()
