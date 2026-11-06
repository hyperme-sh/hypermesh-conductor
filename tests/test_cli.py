import json
import socket
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from conductor.__main__ import main
from conductor.metrics import StepRecord, append_journal

EXAMPLE = Path(__file__).resolve().parents[1] / "examples" / "endpoints.json"


def test_route_command_prints_the_cheap_tier(capsys):
    status = main(["route", "--config", str(EXAMPLE), "--class", "index"])
    assert status == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["job_class"] == "index"
    assert payload["tier"] == "cheap"
    assert payload["model_id"] == "local-small"
    assert payload["prefix_sha256"]


def test_upgrade_on_the_command_line_is_rejected(capsys):
    status = main(["route", "--config", str(EXAMPLE), "--class", "classify", "--suggest", "frontier"])
    assert status == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["tier"] == "cheap"
    assert payload["suggestion_accepted"] is False
    assert payload["reason"].startswith("rejected out-of-policy upgrade")


def test_summary_command_prints_the_split(tmp_path, capsys):
    journal = tmp_path / "journal.jsonl"
    append_journal(
        journal,
        StepRecord(
            task_id="task-1",
            step_id="step-1",
            job_class="parse",
            tier="cheap",
            model_id="local-small",
            reason="policy tier for parse is cheap",
            suggestion_tier=None,
            suggestion_accepted=None,
            input_tokens=10,
            output_tokens=2,
            cache_hit_tokens=4,
            latency_ms=3,
            prefix_sha256="c" * 64,
            usage_status="measured",
        ),
    )
    status = main(["summary", "--journal", str(journal)])
    assert status == 0
    payload = json.loads(capsys.readouterr().out)
    assert payload["tasks"][0]["input_tokens"] == 10
    assert payload["totals"]["by_tier"]["cheap"]["output_tokens"] == 2
    assert payload["totals"]["by_tier"]["frontier"]["steps"] == 0
    assert payload["totals"]["cache_hit_rate"] == 0.4


def test_unknown_class_exits_with_a_usage_error(capsys):
    status = main(["route", "--config", str(EXAMPLE), "--class", "dance"])
    assert status == 2
    assert "unknown job class" in capsys.readouterr().err


class _ChatHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        body = json.dumps(
            {
                "choices": [{"message": {"role": "assistant", "content": "noted"}}],
                "usage": {
                    "prompt_tokens": 6,
                    "completion_tokens": 1,
                    "prompt_tokens_details": {"cached_tokens": 2},
                },
            }
        ).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


def test_run_command_journals_provider_usage(tmp_path, monkeypatch, capsys):
    server = ThreadingHTTPServer(("127.0.0.1", 0), _ChatHandler)
    port = server.server_address[1]
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        _wait_for_port(port)
        payload = json.loads(EXAMPLE.read_text(encoding="utf-8"))
        payload["tiers"]["cheap"]["base_url"] = f"http://127.0.0.1:{port}/v1"
        config_path = tmp_path / "endpoints.json"
        config_path.write_text(json.dumps(payload), encoding="utf-8")
        journal = tmp_path / "journal.jsonl"
        monkeypatch.setenv("CONDUCTOR_CHEAP_API_KEY", "test-key-do-not-log")
        status = main(
            [
                "run",
                "--config",
                str(config_path),
                "--journal",
                str(journal),
                "--task",
                "task-1",
                "--step",
                "step-1",
                "--class",
                "parse",
                "--message",
                "name the fields",
            ]
        )
        assert status == 0
        shown = json.loads(capsys.readouterr().out)
        assert shown["tier"] == "cheap"
        assert shown["input_tokens"] == 6
        assert shown["cache_hit_tokens"] == 2
        assert shown["content"] == "noted"
        stored = journal.read_text(encoding="utf-8")
        assert "test-key-do-not-log" not in stored
        assert "noted" not in stored
        assert "name the fields" not in stored
    finally:
        server.shutdown()
        server.server_close()


def _wait_for_port(port: int) -> None:
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.1):
                return
        except OSError:
            time.sleep(0.01)
    raise AssertionError("local endpoint did not accept connections")


def test_help_has_no_key_flag(capsys):
    try:
        main(["--help"])
    except SystemExit as exc:
        assert exc.code == 0
    text = capsys.readouterr().out
    assert "api-key" not in text
    assert "route" in text
    assert "summary" in text
