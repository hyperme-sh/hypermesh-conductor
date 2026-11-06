"""Command line for routing preview, one step, and the measurement summary."""

from __future__ import annotations

import argparse
import json
import sys

from conductor.config import load_config
from conductor.errors import ConductorError
from conductor.metrics import read_journal, summarize
from conductor.runner import execute_step, preview


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="conductor", description="Route a Conductor step and record token use.")
    sub = parser.add_subparsers(dest="command", required=True)

    route_parser = sub.add_parser("route", help="classify a step and print the tier, model, and reason")
    route_parser.add_argument("--config", required=True)
    route_parser.add_argument("--class", dest="job_class", required=True)
    route_parser.add_argument("--suggest", default=None)

    run_parser = sub.add_parser("run", help="call the selected endpoint and append a journal row")
    run_parser.add_argument("--config", required=True)
    run_parser.add_argument("--journal", required=True)
    run_parser.add_argument("--task", required=True)
    run_parser.add_argument("--step", required=True)
    run_parser.add_argument("--class", dest="job_class", required=True)
    run_parser.add_argument("--suggest", default=None)
    run_parser.add_argument("--message", action="append", default=None)
    run_parser.add_argument("--input", dest="embedding_input", default=None)

    summary_parser = sub.add_parser("summary", help="tokens per task, tier split, and cache hit rate")
    summary_parser.add_argument("--journal", required=True)

    args = parser.parse_args(argv)
    try:
        if args.command == "route":
            payload = preview(load_config(args.config), args.job_class, suggestion_tier=args.suggest)
        elif args.command == "summary":
            payload = summarize(read_journal(args.journal))
        else:
            messages = None
            if args.message:
                messages = [{"role": "user", "content": text} for text in args.message]
            result = execute_step(
                load_config(args.config),
                task_id=args.task,
                step_id=args.step,
                job_class=args.job_class,
                messages=messages,
                embedding_input=args.embedding_input,
                suggestion_tier=args.suggest,
                journal_path=args.journal,
            )
            payload = result.record.to_dict()
            payload["content"] = result.content
            _emit(payload)
            if result.record.usage_status != "measured" and result.record.error:
                return 1
            return 0
    except ConductorError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    _emit(payload)
    return 0


def _emit(payload: dict) -> None:
    print(json.dumps(payload, indent=2, sort_keys=True, ensure_ascii=False))


if __name__ == "__main__":
    raise SystemExit(main())
