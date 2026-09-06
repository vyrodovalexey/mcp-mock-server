// k6 load script for MOCK-901 / MOCK-902 (test asset, TASK-029).
//
// Open-model (constant-arrival-rate) load against a running perfserver. The
// offered rate is fixed and independent of server latency, which is mandatory
// for a latency-under-load measurement: a closed VU model would let the server
// throttle the offered rate and hide the very tail the SLO cares about.
//
// Env vars:
//   TARGET_URL   full MCP endpoint, e.g. http://127.0.0.1:PORT/mcp
//   RATE         steady-state offered rps (default 20000)
//   DURATION     steady-state duration (default 60s)
//   WARMUP       warmup duration, discarded (default 30s)
//   MODE         "steady" (single rate) or "ramp" (stepped saturation search)
//   P99_MS       abort ceiling in ms (generous; verdict ceiling applied in analysis)
//
// A steady run has two scenarios: a warmup that carries a tag so its samples can
// be excluded from analysis, and the measured steady scenario. Percentiles are
// computed from the raw JSON output (--out json=...), never from this summary.

import http from 'k6/http';
import { check } from 'k6';

const TARGET_URL = __ENV.TARGET_URL;
const RATE = parseInt(__ENV.RATE || '20000', 10);
const DURATION = __ENV.DURATION || '60s';
const WARMUP = __ENV.WARMUP || '30s';
const MODE = __ENV.MODE || 'steady';
const P99_MS = parseInt(__ENV.P99_MS || '250', 10);

if (!TARGET_URL) {
  throw new Error('TARGET_URL env var is required');
}

// One canonical tools/call echo request with a complete _meta envelope so it
// passes any validateMeta mode. Built once at init; the body is constant.
const BODY = JSON.stringify({
  jsonrpc: '2.0',
  id: 1,
  method: 'tools/call',
  params: {
    name: 'echo',
    arguments: { msg: 'perf' },
    _meta: {
      protocolVersion: '2026-07-28',
      clientCapabilities: {},
      clientInfo: { name: 'k6-perf', version: '0' },
    },
  },
});

const PARAMS = {
  headers: { 'Content-Type': 'application/json' },
};

// preAllocatedVUs must comfortably exceed rate*latency so the generator is never
// connection- or VU-starved (else it, not the server, is the bottleneck).
function steadyScenarios() {
  return {
    warmup: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      duration: WARMUP,
      preAllocatedVUs: Math.max(200, Math.ceil(RATE / 20)),
      maxVUs: Math.max(2000, Math.ceil(RATE / 4)),
      tags: { phase: 'warmup' },
      exec: 'call',
    },
    steady: {
      executor: 'constant-arrival-rate',
      rate: RATE,
      timeUnit: '1s',
      startTime: WARMUP,
      duration: DURATION,
      preAllocatedVUs: Math.max(200, Math.ceil(RATE / 20)),
      maxVUs: Math.max(2000, Math.ceil(RATE / 4)),
      tags: { phase: 'steady' },
      exec: 'call',
    },
  };
}

// Ramp: stepped offered rates to find the knee where achieved < offered.
function rampScenarios() {
  const steps = (__ENV.RAMP_STEPS || '10000,20000,40000,80000,120000')
    .split(',')
    .map((s) => parseInt(s, 10));
  const stepDur = __ENV.STEP_DURATION || '20s';
  const stepSecs = parseInt(stepDur, 10);
  const scen = {};
  steps.forEach((r, i) => {
    scen[`step_${r}`] = {
      executor: 'constant-arrival-rate',
      rate: r,
      timeUnit: '1s',
      startTime: `${i * stepSecs}s`,
      duration: stepDur,
      preAllocatedVUs: Math.max(200, Math.ceil(r / 20)),
      maxVUs: Math.max(2000, Math.ceil(r / 3)),
      tags: { phase: 'ramp', offered: String(r) },
      exec: 'call',
    };
  });
  return scen;
}

export const options = {
  discardResponseBodies: false,
  scenarios: MODE === 'ramp' ? rampScenarios() : steadyScenarios(),
  thresholds: {
    // Abort ceilings (generous). The real verdict thresholds (25ms / 50ms) are
    // applied in analysis against the raw data, never relaxed here.
    'http_req_failed{phase:steady}': [{ threshold: 'rate<0.01', abortOnFail: true, delayAbortEval: '5s' }],
    'http_req_duration{phase:steady}': [`p(99)<${P99_MS}`],
  },
};

export function call() {
  const res = http.post(TARGET_URL, BODY, PARAMS);
  check(res, {
    'status 200': (r) => r.status === 200,
    'has result': (r) => r.body && r.body.indexOf('"result"') !== -1,
  });
}
