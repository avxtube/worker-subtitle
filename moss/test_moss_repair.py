from types import SimpleNamespace
import unittest
from server import transcribe_with_timestamps


class MossRepairTests(unittest.TestCase):
    def test_speech_uses_one_constrained_model_call(self):
        calls = []
        def transcribe(audio, **kwargs):
            calls.append(kwargs)
            return SimpleNamespace(text="[0.72][S02]こんにちは[2.34]" if kwargs.get("timed_output") else "[S02]こんにちは", generated_tokens=25)
        result = transcribe_with_timestamps(SimpleNamespace(transcribe=transcribe), "audio", "prompt", 4096)
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["timed_output"], "auto")
        self.assertEqual(result["backend"], "moss-constrained")
        self.assertEqual(result["segments"][0]["start"], .72)
        self.assertEqual(result["segments"][0]["speaker"], "S02")

    def test_silence_does_not_force_speech(self):
        def transcribe(audio, **kwargs):
            self.assertEqual(kwargs["timed_output"], "auto")
            return SimpleNamespace(text="", generated_tokens=1)
        result = transcribe_with_timestamps(SimpleNamespace(transcribe=transcribe), "audio", "prompt", 4096)
        self.assertEqual(result["segments"], [])

    def test_failed_repair_is_not_silence(self):
        def transcribe(audio, **kwargs):
            return SimpleNamespace(text="[S01]words", generated_tokens=20)
        result = transcribe_with_timestamps(SimpleNamespace(transcribe=transcribe), "audio", "prompt", 4096)
        self.assertIn("error", result)


if __name__ == "__main__":
    unittest.main()
