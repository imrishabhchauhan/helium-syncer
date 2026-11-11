// Checks the helper extension's script without a browser: the browser's own
// calls are stood in for, and the script is run against them.
//
//   node --test extension/
//
// What matters here is power. On a laptop on battery this worker must stop
// holding itself awake, and must still come back the moment the person
// changes a bookmark.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));

// run loads background.js against stand-ins and gives back the handles the
// tests need.
function run() {
  const listeners = {};
  const on = (name) => ({ addListener: (f) => { (listeners[name] ||= []).push(f); } });
  const alarms = [];
  let sockets = [];

  const chrome = {
    bookmarks: {
      onCreated: on("bm.created"), onRemoved: on("bm.removed"), onChanged: on("bm.changed"),
      onMoved: on("bm.moved"), onChildrenReordered: on("bm.reordered"),
      getTree: async () => [{ id: "0", children: [] }],
    },
    tabs: { onActivated: on("tab.activated"), onCreated: on("tab.created"), onRemoved: on("tab.removed"),
      onUpdated: on("tab.updated"), onMoved: on("tab.moved"), onAttached: on("tab.attached"),
      onDetached: on("tab.detached"), onReplaced: on("tab.replaced"), query: async () => [] },
    tabGroups: { onCreated: on("g.created"), onUpdated: on("g.updated"), onRemoved: on("g.removed"),
      onMoved: on("g.moved"), query: async () => [], get: async () => ({}) },
    windows: { onCreated: on("w.created"), onRemoved: on("w.removed"), onFocusChanged: on("w.focus"),
      getAll: async () => [], WINDOW_ID_NONE: -1 },
    runtime: { onStartup: on("startup"), onInstalled: on("installed"), id: "test" },
    alarms: {
      create: (name, opts) => alarms.push({ name, ...opts }),
      onAlarm: on("alarm"),
    },
    storage: {
      local: { get: async () => ({}), set: async () => {} },
      session: { get: async () => ({}), set: async () => {}, remove: async () => {} },
    },
  };

  class FakeSocket {
    constructor() {
      this.readyState = 1; // OPEN
      this.sent = [];
      sockets.push(this);
      queueMicrotask(() => this.onopen && this.onopen());
    }
    send(text) { this.sent.push(JSON.parse(text)); }
    close() { this.readyState = 3; }
  }
  FakeSocket.OPEN = 1;

  const timers = [];
  const src = readFileSync(join(here, "background.js"), "utf8");
  const body = `"use strict";\n${src}\n;return { fire, alarms, sockets: () => sockets, lastAlarm: () => alarms[alarms.length - 1] };`;
  const fire = (name, ...args) => (listeners[name] || []).forEach((f) => f(...args));
  const make = new Function("chrome", "WebSocket", "fire", "alarms", "sockets", "setInterval", "clearInterval", body);
  const fakeSetInterval = (f, ms) => { const id = timers.length + 1; timers.push({ id, f, ms, live: true }); return id; };
  const fakeClearInterval = (id) => { const t = timers.find((x) => x.id === id); if (t) t.live = false; };
  const api = make(chrome, FakeSocket, fire, alarms, sockets, fakeSetInterval, fakeClearInterval);
  return { ...api, fire, alarms, timers, sockets: () => sockets };
}

const settle = () => new Promise((r) => setTimeout(r, 5));
const livePings = (h) => h.timers.filter((t) => t.live && t.ms === 20000).length;
const lastAlarm = (h) => h.alarms[h.alarms.length - 1];

test("plugged in, it holds itself awake so changes arrive at once", async () => {
  const h = run();
  await settle();
  assert.equal(livePings(h), 1, "it should be sending something every 20 seconds");
  assert.equal(lastAlarm(h).periodInMinutes, 0.5);
});

test("on battery it stops holding itself awake", async () => {
  const h = run();
  await settle();
  const sock = h.sockets()[0];
  sock.onmessage({ data: JSON.stringify({ type: "power", mode: "battery" }) });
  await settle();
  assert.equal(livePings(h), 0, "it is still holding itself awake on battery");
  assert.equal(lastAlarm(h).periodInMinutes, 5, "it still wakes every 30 seconds on battery");
});

test("plugged in again, it comes straight back", async () => {
  const h = run();
  await settle();
  const sock = h.sockets()[0];
  sock.onmessage({ data: JSON.stringify({ type: "power", mode: "battery" }) });
  await settle();
  sock.onmessage({ data: JSON.stringify({ type: "power", mode: "plugged" }) });
  await settle();
  assert.equal(livePings(h), 1, "it did not start holding itself awake again");
  assert.equal(lastAlarm(h).periodInMinutes, 0.5);
});

test("a bookmark change opens the link again, so nothing waits for the alarm", async () => {
  const h = run();
  await settle();
  const before = h.sockets().length;
  h.sockets()[0].readyState = 3; // the worker slept and the link closed
  h.fire("bm.created", "42", { title: "x", url: "https://example.com/" });
  await settle();
  assert.ok(h.sockets().length > before, "a change did not open the link again");
});
