"""Drives the browsing that flowlab records.

For each site it starts a fresh flowlab (so in phantom mode every site gets
a fresh tunnel connection - the start of a connection is what DPI inspects
most closely), has a headless Edge with its own throwaway profile load the
page through it, waits for the page's connections to settle, and stops both.
Then a bulk download and upload measure throughput and record what a large
transfer looks like.

    python -I tools/flowlab/run.py --mode direct --rounds 2
    python -I tools/flowlab/run.py --mode phantom --domain zov-svo.space --rounds 2

Recordings go to tools/flowlab/out/<mode>[-<tag>].jsonl, throughput to
tools/flowlab/out/throughput.jsonl.
"""

import argparse
import json
import os
import pathlib
import shutil
import socket
import subprocess
import sys
import tempfile
import time

ROOT = pathlib.Path(__file__).resolve().parents[2]
OUT = ROOT / "tools" / "flowlab" / "out"
FLOWLAB = ROOT / "bin" / "flowlab.exe"
EDGE = pathlib.Path(r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe")

# Reachable from Russia without a tunnel, so both modes load the same pages.
SITES = [
    "https://www.wikipedia.org/", "https://github.com/", "https://stackoverflow.com/",
    "https://habr.com/ru/", "https://ya.ru/", "https://mail.ru/", "https://vk.com/",
    "https://lenta.ru/", "https://www.rbc.ru/", "https://ria.ru/", "https://www.gismeteo.ru/",
    "https://www.avito.ru/", "https://www.cloudflare.com/", "https://www.mozilla.org/",
    "https://www.python.org/", "https://go.dev/", "https://www.microsoft.com/",
    "https://www.apple.com/", "https://www.amazon.com/", "https://aliexpress.ru/",
]

DOWN_URL = "https://speed.cloudflare.com/__down?bytes=30000000"
UP_URL = "https://speed.cloudflare.com/__up"
UP_BYTES = 10_000_000


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_listening(port, timeout=30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=1):
                return True
        except OSError:
            time.sleep(0.3)
    return False


def kill_tree(proc):
    if proc.poll() is None:
        subprocess.run(["taskkill", "/T", "/F", "/PID", str(proc.pid)],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    proc.wait()


def start_flowlab(args, out, label, attempts=3):
    """Starts flowlab; in phantom mode that includes connecting to the
    server. A failure there is itself a measurement - the server unreachable
    from this network at that moment - so it is logged to failures.jsonl and
    retried after a pause rather than ending the run."""
    for attempt in range(1, attempts + 1):
        port = free_port()
        cmd = [str(pathlib.Path(args.flowlab).resolve()), "-mode", args.mode, "-listen", f"127.0.0.1:{port}",
               "-out", str(out), "-label", label, "-iface", str(args.iface)]
        if args.mode == "phantom":
            cmd += ["-domain", args.domain]
            if args.conns:
                cmd += ["-conns", str(args.conns)]
        log = open(OUT / "flowlab.log", "a")
        proc = subprocess.Popen(cmd, stdout=log, stderr=log)
        if wait_listening(port):
            return proc, port
        kill_tree(proc)
        with open(OUT / "failures.jsonl", "a") as f:
            f.write(json.dumps({"t": time.time(), "mode": args.mode, "tag": args.tag,
                                "label": label, "attempt": attempt}) + "\n")
        print(f"  flowlab did not start (attempt {attempt}/{attempts}) - see flowlab.log", flush=True)
        time.sleep(30)
    raise RuntimeError(f"flowlab did not start for {label}")


def browse(args, out, site, profile):
    proc, port = start_flowlab(args, out, "site:" + site)
    try:
        edge = subprocess.Popen(
            # Background services off: a fresh Edge profile otherwise opens
            # a dozen telemetry/update connections per page, in both modes.
            [str(EDGE), "--headless=new", "--disable-gpu", "--no-first-run",
             "--no-default-browser-check", "--disable-background-networking",
             "--disable-component-update", "--disable-sync", "--no-pings",
             "--disable-domain-reliability", "--disable-client-side-phishing-detection",
             f"--user-data-dir={profile}",
             f"--proxy-server=http://127.0.0.1:{port}", "--proxy-bypass-list=<-loopback>",
             "--dump-dom", site],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            edge.wait(timeout=20)
        except subprocess.TimeoutExpired:
            pass
        time.sleep(3)  # let keep-alive connections show their idle tail
        kill_tree(edge)
        time.sleep(1.5)  # flowlab flushes once a second
    finally:
        kill_tree(proc)


def bulk(args, out, label, up):
    proc, port = start_flowlab(args, out, label)
    try:
        cmd = ["curl", "-s", "-o", os.devnull, "-w", "%{size_download} %{size_upload} %{time_total}",
               "--max-time", "120", "-x", f"http://127.0.0.1:{port}"]
        if up:
            body = OUT / "upload.bin"
            if not body.exists() or body.stat().st_size != UP_BYTES:
                body.write_bytes(os.urandom(UP_BYTES))
            cmd += ["--data-binary", f"@{body}", UP_URL]
        else:
            cmd += [DOWN_URL]
        res = subprocess.run(cmd, capture_output=True, text=True)
        time.sleep(1.5)
    finally:
        kill_tree(proc)
    try:
        down, upl, total = res.stdout.split()
        size = float(upl if up else down)
        mbit = size * 8 / float(total) / 1e6
    except ValueError:
        size, total, mbit = 0, 0, 0
    rec = {"t": time.time(), "mode": args.mode, "tag": args.tag, "dir": "up" if up else "down",
           "bytes": size, "seconds": float(total or 0), "mbit": round(mbit, 2)}
    with open(OUT / "throughput.jsonl", "a") as f:
        f.write(json.dumps(rec) + "\n")
    print(f"  {label}: {mbit:.1f} Mbit/s", flush=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", choices=["direct", "phantom"], required=True)
    ap.add_argument("--domain", default="zov-svo.space")
    ap.add_argument("--iface", type=int, default=4, help="physical interface index (see Get-NetAdapter)")
    ap.add_argument("--rounds", type=int, default=2)
    ap.add_argument("--conns", type=int, default=0, help="phantom: pool size override")
    ap.add_argument("--tag", default="", help="suffix for the output file, e.g. bbr")
    ap.add_argument("--flowlab", default=str(FLOWLAB),
                    help="flowlab binary - a build of the client code under test")
    ap.add_argument("--bulk-only", action="store_true")
    ap.add_argument("--bulk-repeats", type=int, default=2)
    args = ap.parse_args()

    OUT.mkdir(parents=True, exist_ok=True)
    out = OUT / (args.mode + (f"-{args.tag}" if args.tag else "") + ".jsonl")
    profile = pathlib.Path(tempfile.mkdtemp(prefix="flowlab-edge-"))
    try:
        if not args.bulk_only:
            for r in range(args.rounds):
                for i, site in enumerate(SITES):
                    print(f"[{args.mode}] round {r + 1}/{args.rounds} {i + 1}/{len(SITES)} {site}", flush=True)
                    browse(args, out, site, profile)
        for _ in range(args.bulk_repeats):
            bulk(args, out, "bulk:down", up=False)
            bulk(args, out, "bulk:up", up=True)
    finally:
        shutil.rmtree(profile, ignore_errors=True)
    print("done:", out, flush=True)


if __name__ == "__main__":
    sys.exit(main())
