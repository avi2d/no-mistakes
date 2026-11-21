#!/usr/bin/env python3
import argparse
import collections
import datetime
import json
from pathlib import Path
import re
import sqlite3
import subprocess

FIX_TRIGGERS = {"auto_fix", "user_fix"}
HANDOFF_STATUSES = {"awaiting_approval", "fix_review", "completed", "skipped"}
HUNK = re.compile(rb"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")
ATTEMPTS_SQL = """
    SELECT a.run_id, a.round, a.agent || '/' || a.configured_model AS profile,
           a.model, a.duration_ms
    FROM agent_invocations a
    JOIN runs r ON r.id = a.run_id
    WHERE r.created_at >= ? AND a.step_name = 'review' AND a.purpose = 'review-fix'
      AND a.round >= 2 AND a.configured_model IS NOT NULL
"""
REVIEW_SQL = """
    SELECT p.working_path, r.repo_id, s.status, s.findings_json, s.id
    FROM step_results s
    JOIN runs r ON r.id = s.run_id
    JOIN repos p ON p.id = r.repo_id
    WHERE s.run_id = ? AND s.step_name = 'review'
"""
ROUNDS_SQL = """
    SELECT round, trigger_type, findings_json, starting_head_sha, reviewed_head_sha, duration_ms
    FROM step_rounds WHERE step_result_id = ? ORDER BY created_at, rowid
"""


def findings(raw):
    return (json.loads(raw).get("findings") or []) if raw else []


def changed_lines(repo, before, after, path):
    result = subprocess.run(
        ["git", "-C", str(repo), "--literal-pathspecs", "diff", "--no-ext-diff",
         "--no-textconv", "--no-color", "--no-renames", "--diff-algorithm=myers",
         "--no-indent-heuristic", "--unified=0", before, after, "--", path],
        capture_output=True, check=False,
    )
    if result.returncode:
        return None
    spans = []
    for line in result.stdout.splitlines():
        if match := HUNK.match(line):
            start = int(match[1])
            spans.append(range(start, start + int(match[2] or 1)))
    return spans


def count_on_fix_lines(repos, before, after, anchors):
    if not before or not after:
        return None
    if before == after or not anchors:
        return 0
    if not all(re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", sha) for sha in (before, after)):
        return None
    by_path = collections.defaultdict(list)
    for path, line in anchors:
        by_path[path].append(line)
    for repo in repos:
        if not repo.is_dir():
            continue
        changed = {path: changed_lines(repo, before, after, path) for path in by_path}
        if all(spans is not None for spans in changed.values()):
            return sum(
                any(line in span for span in changed[path])
                for path, lines in by_path.items() for line in lines
            )
    return None


def measure_run(db, db_path, run_id, attempts):
    stats = collections.Counter(runs=1)
    review = db.execute(REVIEW_SQL, (run_id,)).fetchone()
    if review is None:
        raise ValueError("trial invocation has no Review step")
    worktree, repo_id, status, handoff_json, step_id = review
    repos = (db_path.parent / "repos" / (repo_id + ".git"), Path(worktree))
    if status in HANDOFF_STATUSES:
        stats["handoff_runs"] = 1
        stats["handoff_errors"] = sum(f["severity"] == "error" for f in findings(handoff_json))
    else:
        stats["review_without_handoff_runs"] = 1
    profile = attempts[0]["profile"]
    served = {a["model"].rsplit("/", 1)[-1] for a in attempts if a["model"]}
    stats["served_model_unreported_runs"] = int(not served)
    stats["served_model_mismatch_runs"] = int(bool(served - {profile.rsplit("/", 1)[-1]}))
    stats["fix_invocation_ms"] = sum(a["duration_ms"] for a in attempts)
    trial_rounds = {a["round"] for a in attempts}
    rounds_with_output = set()
    seen = set()
    for number, trigger, raw, before, after, duration in db.execute(ROUNDS_SQL, (step_id,)):
        items = findings(raw)
        new_anchors = []
        for item in items:
            path, line = item.get("file"), item.get("line", 0)
            key = (path, " ".join(item["description"].split()))
            if key not in seen and not item.get("carried") and path and line > 0:
                new_anchors.append((path, line))
            seen.add(key)
        stats["review_rounds"] += 1
        stats["rounds_2_plus"] += number >= 2
        if number not in trial_rounds or trigger not in FIX_TRIGGERS:
            continue
        rounds_with_output.add(number)
        stats["fix_rounds"] += 1
        stats["fix_round_ms"] += duration
        count = count_on_fix_lines(repos, before, after, new_anchors)
        if count is None:
            stats["unattributed_fix_rounds"] += 1
        else:
            stats["attributed_fix_rounds"] += 1
            stats["new_findings_on_fix_lines"] += count
    stats["fix_rounds_without_output"] = len(trial_rounds - rounds_with_output)
    return stats


def measure_machine(path, since, arms):
    with sqlite3.connect(path.as_uri() + "?mode=ro", uri=True) as db:
        db.row_factory = sqlite3.Row
        db.execute("BEGIN")
        by_run = collections.defaultdict(list)
        for row in db.execute(ATTEMPTS_SQL, (since,)):
            by_run[row["run_id"]].append(row)
        totals = {arm: collections.Counter() for arm in arms}
        mixed = 0
        for run, attempts in by_run.items():
            profiles = {a["profile"] for a in attempts}
            if not profiles.intersection(arms):
                continue
            if len(profiles) != 1:
                mixed += 1
                continue
            totals[attempts[0]["profile"]].update(measure_run(db, path, run, attempts))
        return totals, mixed


def summarize(s):
    def average(numerator, denominator, scale=1):
        return round(s[numerator] / s[denominator] / scale, 3) if s[denominator] else None

    return {
        "runs": s["runs"],
        "review_rounds_per_run": average("review_rounds", "runs"),
        "rounds_2_plus_per_run": average("rounds_2_plus", "runs"),
        "new_findings_on_fix_written_lines": s["new_findings_on_fix_lines"] if s["attributed_fix_rounds"] else None,
        "new_findings_per_attributed_fix_round": average("new_findings_on_fix_lines", "attributed_fix_rounds"),
        "attributed_fix_rounds": s["attributed_fix_rounds"],
        "unattributed_fix_rounds": s["unattributed_fix_rounds"],
        "error_findings_left_at_handoff": s["handoff_errors"] if s["handoff_runs"] else None,
        "error_findings_per_handoff_run": average("handoff_errors", "handoff_runs"),
        "handoff_runs": s["handoff_runs"],
        "review_without_handoff_runs": s["review_without_handoff_runs"],
        "fix_rounds_timed": s["fix_rounds"],
        "fix_round_wall_seconds_total": s["fix_round_ms"] / 1000 if s["fix_rounds"] else None,
        "fix_round_wall_seconds_per_round": average("fix_round_ms", "fix_rounds", 1000),
        "fix_rounds_without_output": s["fix_rounds_without_output"],
        "fix_invocation_seconds_total": s["fix_invocation_ms"] / 1000 if s["runs"] else None,
        "served_model_unreported_runs": s["served_model_unreported_runs"],
        "served_model_mismatch_runs": s["served_model_mismatch_runs"],
    }


def main():
    parser = argparse.ArgumentParser(description="Measure a later-round fixer trial from local SQLite and Git records.")
    parser.add_argument("--since", required=True, help="trial start in UTC, e.g. 2026-11-21T18:00:00Z")
    parser.add_argument("--arm", action="append", required=True, metavar="AGENT/MODEL")
    parser.add_argument("databases", nargs="+", type=Path, metavar="STATE.SQLITE")
    args = parser.parse_args()
    if len(set(args.arm)) != 2 or len(args.arm) != 2 or any("/" not in arm for arm in args.arm):
        parser.error("give two distinct --arm AGENT/MODEL values")
    try:
        since = int(datetime.datetime.strptime(args.since, "%Y-%m-%dT%H:%M:%SZ")
                    .replace(tzinfo=datetime.timezone.utc).timestamp())
    except ValueError:
        parser.error("--since must be UTC in YYYY-MM-DDTHH:MM:SSZ form")
    paths = [path.resolve() for path in args.databases]
    if len(set(paths)) != len(paths):
        parser.error("give each machine's database only once")
    combined = {arm: collections.Counter() for arm in args.arm}
    machines = []
    for path in paths:
        try:
            totals, mixed = measure_machine(path, since, args.arm)
        except (sqlite3.Error, OSError, ValueError) as error:
            parser.exit(1, f"could not measure {path}: {error}\n")
        machines.append({"database": str(path), "mixed_profile_runs_excluded": mixed,
                         "arms": {arm: summarize(s) for arm, s in totals.items()}})
        for arm, stats in totals.items():
            combined[arm].update(stats)
    print(json.dumps({"since": args.since, "machines": machines,
                      "pooled": {arm: summarize(s) for arm, s in combined.items()}}, indent=2))


if __name__ == "__main__":
    main()
