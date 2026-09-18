import unittest
from moss_transcribe_diarize.transcript_parser import parse_transcript

class SharedBoundaryTests(unittest.TestCase):
    def test_empty_prefix_preserves_real_text_and_time(self):
        result=parse_transcript('[0.00][S01][0.06][S01]I[0.12]')
        self.assertEqual([(s.start,s.end,s.text) for s in result],[(0.06,0.12,'I')])
    def test_ordinary_segments_unchanged(self):
        result=parse_transcript('[0.00][S01]Hello[0.50][0.60][S02]World[1.00]')
        self.assertEqual([s.text for s in result],['Hello','World'])
    def test_timestamp_only_is_still_empty(self):
        self.assertEqual(parse_transcript('[0.00][S01][0.06][0.10][S01][0.20]'),[])
if __name__=='__main__': unittest.main()
