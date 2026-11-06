from conductor.client import parse_usage


def test_openai_cached_tokens_are_recorded():
    usage = parse_usage(
        {
            "usage": {
                "prompt_tokens": 20,
                "completion_tokens": 5,
                "prompt_tokens_details": {"cached_tokens": 16},
                "prompt_cache_hit_tokens": 1,
            }
        }
    )
    assert usage.status == "measured"
    assert usage.input_tokens == 20
    assert usage.output_tokens == 5
    assert usage.cache_hit_tokens == 16


def test_prompt_cache_hit_tokens_are_the_fallback():
    usage = parse_usage(
        {"usage": {"prompt_tokens": 10, "completion_tokens": 2, "prompt_cache_hit_tokens": 4}}
    )
    assert usage.cache_hit_tokens == 4


def test_reported_zero_cache_hits_stay_zero():
    usage = parse_usage(
        {"usage": {"prompt_tokens": 10, "completion_tokens": 2, "prompt_tokens_details": {"cached_tokens": 0}}}
    )
    assert usage.cache_hit_tokens == 0


def test_missing_usage_is_uncertain_rather_than_zero():
    usage = parse_usage({"choices": [{"message": {"content": "ok"}}]})
    assert usage.status == "uncertain"
    assert usage.input_tokens is None
    assert usage.output_tokens is None
    assert usage.cache_hit_tokens is None


def test_partial_usage_is_uncertain():
    usage = parse_usage({"usage": {"prompt_tokens": 12}})
    assert usage.status == "uncertain"
    assert usage.input_tokens is None
    assert usage.cache_hit_tokens is None


def test_string_counts_are_not_coerced():
    usage = parse_usage({"usage": {"prompt_tokens": "12", "completion_tokens": 1}})
    assert usage.status == "uncertain"
    assert usage.input_tokens is None


def test_negative_counts_are_uncertain():
    usage = parse_usage({"usage": {"prompt_tokens": -1, "completion_tokens": 1}})
    assert usage.status == "uncertain"


def test_embeddings_without_completion_tokens_record_zero_output():
    usage = parse_usage({"usage": {"prompt_tokens": 8, "total_tokens": 8}}, embeddings=True)
    assert usage.status == "measured"
    assert usage.input_tokens == 8
    assert usage.output_tokens == 0
    assert usage.cache_hit_tokens is None
