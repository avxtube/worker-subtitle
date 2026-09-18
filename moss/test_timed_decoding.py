import unittest
from unittest.mock import patch
from types import SimpleNamespace
import torch
from lmformatenforcer import RegexParser
from lmformatenforcer.characterlevelparser import CharacterLevelParserConfig
from moss_transcribe_diarize.timed_decoding import TIMED_TRANSCRIPT, REQUIRED_TIMED_TRANSCRIPT, AUTO_TIMED_TRANSCRIPT, TimedRegexParser, TimedLogitsProcessor


def accepts(text):
    parser = TimedRegexParser(TIMED_TRANSCRIPT, CharacterLevelParserConfig(alphabet="0123456789.S[]helloこんにちはไทย "))
    for char in text:
        if char not in parser.get_allowed_characters():
            return False
        parser = parser.add_character(char)
    return parser.can_end()


class TimedDecodingTests(unittest.TestCase):
    def test_auto_mode_preserves_original_silence_decision(self):
        with patch('moss_transcribe_diarize.timed_decoding.timed_prefix_function', return_value=lambda row, ids: [1, 2]):
            processor = TimedLogitsProcessor(SimpleNamespace(eos_token_id=0), allow_initial_silence=True)
            result = processor(torch.tensor([[8]]), torch.tensor([[9., 3., 2., 1.]]))
            self.assertEqual(result.argmax().item(), 0)

    def test_rejected_speaker_token_does_not_turn_speech_into_silence(self):
        with patch('moss_transcribe_diarize.timed_decoding.timed_prefix_function', return_value=lambda row, ids: [1, 2]):
            processor = TimedLogitsProcessor(SimpleNamespace(eos_token_id=0), allow_initial_silence=True)
            # Untimed speaker token 3 wins originally; EOS is only second best.
            result = processor(torch.tensor([[8]]), torch.tensor([[8., 3., 2., 9.]]))
            self.assertEqual(result.argmax().item(), 1)
            self.assertTrue(torch.isneginf(result[0,0]))

    def test_auto_mode_allows_timestamp_only_empty_segment(self):
        parser = TimedRegexParser(AUTO_TIMED_TRANSCRIPT)
        for char in '[0.00][S01][1.00]':
            self.assertIn(char, parser.get_allowed_characters())
            parser = parser.add_character(char)
        self.assertTrue(parser.can_end())
    def test_repair_pass_cannot_stop_without_a_segment(self):
        self.assertFalse(TimedRegexParser(REQUIRED_TIMED_TRANSCRIPT).can_end())
    def test_complete_segments_and_silence(self):
        for text in ["", "[0.72][S01]こんにちは[2.34]", "[1.00][S02]ไทย[2.00][2.10][S01]hello[3.00]"]:
            self.assertTrue(accepts(text), text)

    def test_no_untimed_or_incomplete_segments(self):
        for text in ["[S01]hello", "[0.00][S01]hello", "[0.00]hello[1.00]",
                     "[0.00][S01]he[S02]llo[1.00]", "[start][S01]hello[end]"]:
            self.assertFalse(accepts(text), text)


if __name__ == "__main__":
    unittest.main()
