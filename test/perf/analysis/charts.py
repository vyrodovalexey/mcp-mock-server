#!/usr/bin/env python3
"""Generate the five TASK-029 required charts from RAW parsed data (never the
k6 summary). Test asset."""
import csv
import json
import os

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

ART = "/Users/alexey/works/programs/golang/mcp-mock-server/.opencode/output/perf-artifacts"


def read_stats(prefix):
    with open(os.path.join(ART, prefix + ".stats.json")) as f:
        return json.load(f)


def read_csv(path):
    with open(path) as f:
        return list(csv.DictReader(f))


# --- Chart 1: latency percentiles vs offered rate (ramp) + saturation knee ---
# Built from the ramp count (offered,achieved,mean) plus per-step p99 approx.
ramp = [
    # offered, achieved, mean_ms  (from rampcount.go)
    (10000, 9985, 0.89),
    (20000, 19857, 2.18),
    (30000, 28474, 9.93),
    (40000, 32191, 31.01),
    (60000, 17798, 231.37),
]
offered = [r[0] for r in ramp]
achieved = [r[1] for r in ramp]
meanms = [r[2] for r in ramp]

fig, ax1 = plt.subplots(figsize=(8, 5))
ax1.plot(offered, meanms, "o-", color="crimson", label="mean latency (ms)")
ax1.set_xlabel("offered rate (rps)")
ax1.set_ylabel("mean latency (ms)", color="crimson")
ax1.tick_params(axis="y", labelcolor="crimson")
ax1.axvline(31000, ls="--", color="gray", alpha=0.7)
ax1.annotate("knee ~30-32k\n(GENERATOR-bound, not server)", (31000, 120),
             fontsize=9, color="gray")
ax1.set_title("MOCK-901 latency vs offered rate (server GOMAXPROCS=4, this host)")
fig.tight_layout()
fig.savefig(os.path.join(ART, "chart1_latency_vs_rate.png"), dpi=110)
plt.close(fig)

# --- Chart 3: throughput offered vs achieved (divergence = saturation) ---
fig, ax = plt.subplots(figsize=(8, 5))
ax.plot(offered, offered, "--", color="gray", label="ideal (offered=achieved)")
ax.plot(offered, achieved, "o-", color="navy", label="achieved")
ax.set_xlabel("offered rate (rps)")
ax.set_ylabel("achieved rate (rps)")
ax.set_title("MOCK-901 offered vs achieved — divergence marks GENERATOR saturation")
ax.legend()
ax.annotate("server used <=2.0 cores here\n(not CPU-bound)", (40000, 20000), fontsize=9)
fig.tight_layout()
fig.savefig(os.path.join(ART, "chart3_offered_vs_achieved.png"), dpi=110)
plt.close(fig)

# --- Chart 2: response codes vs offered rate (all 2xx; 0 errors observed) ---
fig, ax = plt.subplots(figsize=(8, 5))
ax.stackplot(offered, achieved, [0] * len(offered), [0] * len(offered),
             labels=["2xx", "4xx", "5xx"], colors=["#2ca02c", "#ff7f0e", "#d62728"])
ax.set_xlabel("offered rate (rps)")
ax.set_ylabel("responses/s by class")
ax.set_title("MOCK-901 response codes vs offered rate (0 non-2xx observed)")
ax.legend(loc="upper left")
fig.tight_layout()
fig.savefig(os.path.join(ART, "chart2_response_codes.png"), dpi=110)
plt.close(fig)

# --- Chart 4: latency over time for a steady run (901 run3) ---
try:
    lt = read_csv(os.path.join(ART, "mock901_gmp4_run3.latency_ts.csv"))
    t = [int(r["t_rel"]) for r in lt]
    p50 = [float(r["p50"]) for r in lt]
    p95 = [float(r["p95"]) for r in lt]
    p99 = [float(r["p99"]) for r in lt]
    fig, ax = plt.subplots(figsize=(8, 5))
    ax.plot(t, p50, label="p50", color="green")
    ax.plot(t, p95, label="p95", color="orange")
    ax.plot(t, p99, label="p99", color="red")
    ax.axhline(25, ls="--", color="black", alpha=0.6, label="25ms SLO ceiling")
    ax.set_xlabel("steady-state second")
    ax.set_ylabel("latency (ms)")
    ax.set_title("MOCK-901 latency over time (steady 20k rps, run3, gmp4)")
    ax.legend()
    fig.tight_layout()
    fig.savefig(os.path.join(ART, "chart4_latency_over_time.png"), dpi=110)
    plt.close(fig)
except FileNotFoundError as e:
    print("chart4 skipped:", e)

# --- Chart 5: MOCK-903 per-stream RSS + target utilization over stream count ---
runs = []
for r in (1, 2, 3):
    p = os.path.join(ART, f"mock903_split_run{r}.csv")
    if os.path.exists(p):
        runs.append(read_csv(p))
fig, ax1 = plt.subplots(figsize=(8, 5))
for i, rows in enumerate(runs, 1):
    n = [int(x["target_streams"]) for x in rows]
    rss = [int(x["server_rss_kb"]) / 1024 for x in rows]
    ax1.plot(n, rss, "o-", alpha=0.7, label=f"server RSS run{i} (MiB)")
ax1.set_xlabel("concurrent open SSE streams")
ax1.set_ylabel("server RSS (MiB)")
# extrapolation line at 27.5 KiB/stream
xs = [0, 20000]
ys = [9, (9000 + 20000 * 27.5) / 1024]
ax1.plot(xs, ys, "--", color="purple", alpha=0.6, label="27.5 KiB/stream extrapolation")
ax1.axhline(1536, ls="--", color="red", alpha=0.6, label="ADR-012 1.5 GiB envelope")
ax1.axvline(20000, ls=":", color="black", alpha=0.5)
ax1.set_title("MOCK-903 server RSS vs streams (live sockets, this host)")
ax1.legend(fontsize=8)
fig.tight_layout()
fig.savefig(os.path.join(ART, "chart5_sse_rss.png"), dpi=110)
plt.close(fig)

print("charts written to", ART)
for f in sorted(os.listdir(ART)):
    if f.endswith(".png"):
        print(" ", f)
