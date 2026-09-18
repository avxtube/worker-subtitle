"""Constrain MOSS token selection to timed, speaker-labelled segments."""
from lmformatenforcer import RegexParser
from lmformatenforcer.tokenenforcer import TokenEnforcer, TokenEnforcerTokenizerData
import torch
from transformers import LogitsProcessor

# Values are selected by MOSS from audio; this grammar supplies no fabricated
# timestamps. Empty output remains valid for silence. Bounds are checked by Go.
TIMESTAMP = r"\[[0-9]{1,4}\.[0-9]{2}\]"
TIMED_TRANSCRIPT = rf"({TIMESTAMP}\[S[0-9]{{2}}\][^\[\]]+{TIMESTAMP})*"
REQUIRED_TIMED_TRANSCRIPT = rf"({TIMESTAMP}\[S[0-9]{{2}}\][^\[\]]+{TIMESTAMP})+"
AUTO_TIMED_TRANSCRIPT = rf"({TIMESTAMP}\[S[0-9]{{2}}\][^\[\]]*{TIMESTAMP})+"


class TimedRegexParser(RegexParser):
    def get_allowed_characters(self):
        # RegexParser exposes a Unicode alphabet string. Traversing a large
        # multilingual token trie must intersect sets, not rescan that string
        # for each trie node.
        cache = getattr(self.context, "moss_character_sets", None)
        if cache is None:
            cache = self.context.moss_character_sets = {}
        if self.current_state not in cache:
            cache[self.current_state] = frozenset(super().get_allowed_characters())
        return cache[self.current_state]

    def add_character(self, char):
        child = super().add_character(char)
        return TimedRegexParser(child.context, child.config, child.current_state)

    def _update_alphabet(self, alphabet):
        if getattr(self.context, "moss_alphabet", None) != alphabet:
            super()._update_alphabet(alphabet)
            self.context.moss_alphabet = alphabet
            self.context.moss_character_sets = {}
            self.context.state_character_cache.clear()


def timed_prefix_function(tokenizer, allow_empty_segments=False):
    # Tokenizer vocabulary indexing is expensive; reuse it across audio chunks.
    data = getattr(tokenizer, "_moss_timed_tokenizer_data", None)
    if data is None:
        # Use the stable core API: the library's released HF adapter still
        # imports a tokenization_utils symbol removed in Transformers 5.
        special = set(tokenizer.all_special_ids)
        ids = [i for i in range(len(tokenizer)) if i not in special]
        anchor = tokenizer.encode("0", add_special_tokens=False)
        alone = tokenizer.batch_decode([[i] for i in ids], clean_up_tokenization_spaces=False)
        contextual = tokenizer.batch_decode([anchor + [i] for i in ids], clean_up_tokenization_spaces=False)
        anchor_text = tokenizer.decode(anchor, clean_up_tokenization_spaces=False)
        contextual = [text[len(anchor_text):] for text in contextual]
        vocabulary = [(i, text, len(text) > len(single)) for i, text, single in zip(ids, contextual, alone)]
        decode = lambda values: tokenizer.decode(values, clean_up_tokenization_spaces=False).rstrip("\ufffd")
        data = TokenEnforcerTokenizerData(vocabulary, decode, tokenizer.eos_token_id, False, len(tokenizer))
        tokenizer._moss_timed_tokenizer_data = data
    # Parsing state must be fresh for each generate() call.
    pattern = AUTO_TIMED_TRANSCRIPT if allow_empty_segments else REQUIRED_TIMED_TRANSCRIPT
    enforcer = TokenEnforcer(data, TimedRegexParser(pattern))
    return lambda batch_id, sent: enforcer.get_allowed_tokens(sent.tolist()).allowed_tokens


class TimedLogitsProcessor(LogitsProcessor):
    def __init__(self, tokenizer, allow_initial_silence=False):
        self.allowed = timed_prefix_function(tokenizer, allow_empty_segments=allow_initial_silence)
        self.masks = {}
        self.allow_initial_silence = allow_initial_silence
        self.eos_token_id = tokenizer.eos_token_id
        self.first_step = True

    def __call__(self, input_ids, scores):
        for row in range(input_ids.shape[0]):
            # Only accept immediate silence if it is the model's ORIGINAL
            # top choice. Masking speaker tokens must not turn speech into EOS.
            if self.first_step and self.allow_initial_silence and scores[row].argmax().item() == self.eos_token_id:
                scores[row].fill_(float("-inf"))
                scores[row, self.eos_token_id] = 0
                continue
            allowed = self.allowed(row, input_ids[row])
            # The enforcer reuses allowed-token lists for equivalent grammar
            # states. Avoid reconstructing/transferring a 150k-token mask on
            # every generated Japanese character.
            key = id(allowed)
            if key not in self.masks:
                mask = torch.full_like(scores[row], float("-inf"))
                mask[allowed] = 0
                self.masks[key] = (allowed, mask)
            scores[row] += self.masks[key][1]
        self.first_step = False
        return scores
