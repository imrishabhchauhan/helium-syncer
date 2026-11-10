// Helium Syncer, in the browser.
//
// Helium Syncer syncs your computers by reading and writing Helium's files,
// which it can only do while Helium is closed. Chromium keeps bookmarks in
// memory and rewrites the file when it quits, so anything written underneath
// a running browser is thrown away. This extension is the part that can
// change bookmarks while you are using them, because it asks Helium itself.
//
// The same goes for open tabs: Helium rewrites its session files as you
// browse, so they can only be changed while it is closed. This extension
// reports the open windows and tabs, and opens, closes, moves, pins and
// groups tabs when the app asks, so the two computers show the same tabs
// while both are in use.
//
// It talks to the Helium Syncer app already running on this computer, over a
// WebSocket on a local port. Nothing leaves the computer from here: the app
// does the encryption and the talking to your other computer.

const ADDRESS = "ws://127.0.0.1:47811/live";
const VERSION = 1;
const SEND_AFTER_MS = 800;        // wait for a burst of edits to settle
const TABS_AFTER_MS = 1500;       // wait for a page to finish moving between addresses
const STARTUP_MS = 20000;         // Helium may still be restoring its windows this long after start
const SETTLED_AFTER_MS = 3000;    // after a page loads, how long it may still move by itself
const PING_EVERY_MS = 20000;      // keeps this worker awake (under the 30 s idle limit)
const WAKE_ALARM = "helium-syncer-wake";

let socket = null;
let sendTimer = null;
let tabsTimer = null;
let pingTimer = null;
let applying = false;             // true while we are the ones changing bookmarks
let applyingTabs = false;         // the same for tabs
let backoffMs = 1000;
const workerStart = Date.now();
let lastLayout = "";              // the tabs as last read, for the startup check

// ---------------------------------------------------------------- the tree

// flatten turns Helium's bookmark tree into the flat list the app expects.
// The three permanent folders are marked by folderType, never by name or id:
// names are translated and ids are not fixed.
function flatten(tree) {
  const out = [];
  const walk = (node, parentId, index) => {
    const isRoot = node.id === "0";
    if (!isRoot) {
      const entry = {
        id: node.id,
        index: index,
        title: node.title || "",
        folder: !node.url,
      };
      if (parentId && parentId !== "0") entry.parentId = parentId;
      if (node.url) entry.url = node.url;
      if (node.dateAdded) entry.dateAdded = node.dateAdded;
      if (node.folderType) entry.folderType = node.folderType;
      out.push(entry);
    }
    (node.children || []).forEach((child, i) => walk(child, node.id, i));
  };
  tree.forEach((node) => walk(node, null, 0));
  return out;
}

async function currentTree() {
  return flatten(await chrome.bookmarks.getTree());
}

// ---------------------------------------------------------------- sending

function scheduleSend(why) {
  if (applying) return;           // our own changes; the app already has them
  clearTimeout(sendTimer);
  sendTimer = setTimeout(() => sendTree(why), SEND_AFTER_MS);
}

async function sendTree(why) {
  if (!socket || socket.readyState !== WebSocket.OPEN) return;
  try {
    const nodes = await currentTree();
    socket.send(JSON.stringify({ type: "tree", version: VERSION, nodes: nodes, reason: why || "" }));
  } catch (e) {
    console.warn("Helium Syncer: could not send the bookmarks:", e);
  }
}

// ---------------------------------------------------------------- applying

// apply makes this browser's bookmarks match what the app sent. The app has
// already merged both computers, so this is the answer, not a suggestion.
//
// Order matters: create what is new before moving anything into it, and
// remove last, so a bookmark is never homeless in between.
async function apply(target) {
  applying = true;
  const newId = new Map();        // the app's placeholder -> Helium's id
  try {
    const have = new Map();
    for (const n of await currentTree()) have.set(n.id, n);

    // 1. create the ones this browser has never seen. The app names them
    //    "new:<its own id>" because only Helium can hand out a real id.
    const realId = (id) => newId.get(id) || id;
    for (const want of target) {
      if (!want.id.startsWith("new:")) continue;
      const parent = realId(want.parentId);
      if (!parent) continue;
      const made = await chrome.bookmarks.create({
        parentId: parent,
        title: want.title,
        url: want.folder ? undefined : want.url,
      });
      newId.set(want.id, made.id);
      have.set(made.id, { id: made.id, parentId: parent, index: made.index, title: want.title, url: want.url, folder: want.folder });
    }

    // 2. title and address
    for (const want of target) {
      const id = realId(want.id);
      const now = have.get(id);
      if (!now || want.folderType) continue;       // never touch the permanent folders
      const changes = {};
      if ((now.title || "") !== (want.title || "")) changes.title = want.title || "";
      if (!want.folder && (now.url || "") !== (want.url || "")) changes.url = want.url;
      if (Object.keys(changes).length) await chrome.bookmarks.update(id, changes);
    }

    // 3. where they sit, parents before children
    for (const want of target) {
      const id = realId(want.id);
      const now = have.get(id);
      if (!now || want.folderType || !want.parentId) continue;
      const parent = realId(want.parentId);
      if (now.parentId === parent && now.index === want.index) continue;
      try {
        await chrome.bookmarks.move(id, { parentId: parent, index: want.index });
      } catch (e) {
        console.warn("Helium Syncer: could not move a bookmark:", e);
      }
    }

    // 4. remove what the app says is gone. Deepest first, so removing a
    //    folder never surprises a child we were about to remove ourselves.
    const wanted = new Set(target.map((n) => realId(n.id)));
    const gone = [...have.values()].filter((n) => !wanted.has(n.id) && n.parentId);
    gone.sort((a, b) => depth(have, b) - depth(have, a));
    for (const n of gone) {
      try {
        if (n.folder) await chrome.bookmarks.removeTree(n.id);
        else await chrome.bookmarks.remove(n.id);
      } catch (e) {
        // already gone with its parent
      }
    }
  } finally {
    applying = false;
  }
  return newId;
}

function depth(have, node) {
  let d = 0;
  let cur = node;
  while (cur && cur.parentId && have.has(cur.parentId)) {
    cur = have.get(cur.parentId);
    d++;
    if (d > 64) break;
  }
  return d;
}

// ---------------------------------------------------------------- tabs

// Tabs this extension opened because the other computer has them, by tab
// id: { origin: the address asked for, settled: where the page ended up }.
// A page often moves by itself when it opens (http to https, a login page);
// it is still the page the other computer has, so it is reported with its
// origin until the person takes it somewhere else. Kept in session storage,
// which outlives this worker being stopped and started again.
async function openedTabs() {
  try {
    return (await chrome.storage.session.get("opened")).opened || {};
  } catch (e) {
    return {};
  }
}

async function saveOpened(opened) {
  try {
    await chrome.storage.session.set({ opened });
  } catch (e) {
    // only the redirect memory is lost; the app remembers moves too
  }
}

// readTabs returns every normal window and its tabs, or null when the tabs
// cannot be read in full (no permission to see addresses yet).
async function readTabs() {
  const wins = await chrome.windows.getAll({ populate: true, windowTypes: ["normal"] });
  const opened = await openedTabs();
  const windows = [];
  const used = new Set();
  for (const w of wins) {
    if (w.incognito) continue;
    const tabs = [];
    for (const t of w.tabs || []) {
      const url = t.url || t.pendingUrl;
      if (url === undefined) return null;    // no "tabs" permission: report nothing
      const entry = { id: t.id, url: url, groupId: t.groupId === undefined ? -1 : t.groupId };
      if (t.title) entry.title = t.title;
      if (t.pinned) entry.pinned = true;
      if (t.active) entry.active = true;
      const mark = opened[t.id];
      if (mark && mark.origin !== url && (!mark.settled || mark.settled === url)) entry.origin = mark.origin;
      if (entry.groupId >= 0) used.add(entry.groupId);
      tabs.push(entry);
    }
    windows.push({ id: w.id, tabs: tabs });
  }
  const groups = [];
  if (used.size && chrome.tabGroups) {
    for (const g of await chrome.tabGroups.query({})) {
      if (used.has(g.id)) groups.push({ id: g.id, title: g.title || "", color: g.color, collapsed: !!g.collapsed });
    }
  }
  return { windows: windows, groups: groups };
}

function scheduleTabs(why, after) {
  if (applyingTabs) return;       // our own changes; reported once at the end
  clearTimeout(tabsTimer);
  tabsTimer = setTimeout(() => sendTabs(why), after === undefined ? TABS_AFTER_MS : after);
}

async function sendTabs(why) {
  if (!socket || socket.readyState !== WebSocket.OPEN || applyingTabs) return;
  try {
    const report = await readTabs();
    if (!report) return;
    // Just after a start Helium may still be bringing back its windows. A
    // half-restored list would read as tabs closed, so it is only sent once
    // two looks a second apart agree.
    const layout = JSON.stringify(report.windows.map((w) => w.tabs.map((t) => t.id + " " + t.url)));
    if (Date.now() - workerStart < STARTUP_MS && layout !== lastLayout) {
      lastLayout = layout;
      scheduleTabs(why, 1000);
      return;
    }
    lastLayout = layout;
    // The first report since the browser started shows what Helium restored,
    // which can be an older copy than the synced tabs; the app treats it so.
    // Session storage is empty at each browser start and survives this
    // worker being stopped, so a worker restart is not mistaken for a start.
    let started = true;
    try {
      started = !!(await chrome.storage.session.get("reported")).reported;
    } catch (e) {
      // no session storage: never claim a start
    }
    if (!started) report.fresh = true;
    socket.send(JSON.stringify({ type: "tabs", version: VERSION, tabs: report, reason: why || "" }));
    if (!started) await chrome.storage.session.set({ reported: true }).catch(() => {});
  } catch (e) {
    console.warn("Helium Syncer: could not send the tabs:", e);
  }
}

// applyTabs makes the changes the app asks for. The app names every tab to
// close; any other tab, such as one opened a moment ago, is left alone.
//
// Order matters: open what is new first, close second, then put tabs in
// place, so a window is never left empty in between. A window whose tabs
// all closed on the other computer closes here too, but the last window
// never does: on Windows that would quit Helium.
async function applyTabs(plan) {
  applyingTabs = true;
  clearTimeout(tabsTimer);
  const opened = await openedTabs();
  try {
    const wins = await chrome.windows.getAll({ populate: true, windowTypes: ["normal"] });
    const exists = new Map();
    for (const w of wins) for (const t of w.tabs || []) exists.set(t.id, t);
    const windowIds = new Set(wins.map((w) => w.id));

    // 1. Open new windows and tabs. Each window's wanted order, by tab id.
    const wanted = [];
    for (const pw of plan.windows || []) {
      let winId = windowIds.has(pw.id) ? pw.id : 0;
      const order = [];
      for (const pt of pw.tabs) {
        let id = pt.id && exists.has(pt.id) ? pt.id : 0;
        if (pt.id && !id) continue;            // closed here since the app looked
        if (!winId) {
          // A new window starts with its first tab.
          const made = id
            ? await chrome.windows.create({ tabId: id, focused: false })
            : await chrome.windows.create({ url: pt.url, focused: false });
          winId = made.id;
          windowIds.add(winId);
          if (!id) {
            id = made.tabs[0].id;
            opened[id] = { origin: pt.url };
          }
        } else if (!id) {
          const made = await chrome.tabs.create({ windowId: winId, url: pt.url, active: false, pinned: !!pt.pinned });
          id = made.id;
          opened[id] = { origin: pt.url };
        }
        order.push({ id: id, pinned: !!pt.pinned, group: pt.group || "" });
      }
      if (winId && order.length) wanted.push({ winId: winId, order: order });
    }
    await saveOpened(opened);

    // 2. Close what closed on the other computer.
    const closing = new Set((plan.close || []).filter((id) => exists.has(id)));
    if (closing.size) {
      const now = await chrome.windows.getAll({ populate: true, windowTypes: ["normal"] });
      const left = now.filter((w) => (w.tabs || []).some((t) => !closing.has(t.id)));
      if (left.length === 0 && now.length) {
        // Every tab of every window would close: keep one window open.
        await chrome.tabs.create({ windowId: now[0].id, url: "chrome://newtab/", active: true });
      }
      try {
        await chrome.tabs.remove([...closing]);
      } catch (e) {
        for (const id of closing) await chrome.tabs.remove(id).catch(() => {});
      }
      for (const id of closing) delete opened[id];
      await saveOpened(opened);
    }

    // 3. Pin, then put each tab where it belongs.
    for (const w of wanted) {
      for (const want of w.order) {
        const t = await chrome.tabs.get(want.id).catch(() => null);
        if (t && !!t.pinned !== want.pinned) await chrome.tabs.update(want.id, { pinned: want.pinned }).catch(() => {});
      }
      let current = (await chrome.tabs.query({ windowId: w.winId })).map((t) => t.id);
      for (let i = 0; i < w.order.length; i++) {
        const id = w.order[i].id;
        if (current[i] === id) continue;
        try {
          await chrome.tabs.move(id, { windowId: w.winId, index: i });
        } catch (e) {
          continue;                                // a pinned/unpinned boundary; the next look fixes it
        }
        current = current.filter((x) => x !== id);
        current.splice(i, 0, id);
      }
    }

    // 4. Groups.
    if (chrome.tabGroups) {
      const groupOf = new Map();                   // key -> browser group id
      for (const g of plan.groups || []) if (g.id) groupOf.set(g.key, g.id);
      for (const w of wanted) {
        const members = new Map();                 // key -> tab ids
        const ungroup = [];
        for (const want of w.order) {
          const t = await chrome.tabs.get(want.id).catch(() => null);
          if (!t) continue;
          if (!want.group || want.pinned) {
            if (t.groupId >= 0) ungroup.push(want.id);
            continue;
          }
          if (!members.has(want.group)) members.set(want.group, []);
          members.get(want.group).push(want.id);
        }
        if (ungroup.length) await chrome.tabs.ungroup(ungroup).catch(() => {});
        for (const [key, ids] of members) {
          const known = groupOf.get(key);
          try {
            const gid = known
              ? await chrome.tabs.group({ groupId: known, tabIds: ids })
              : await chrome.tabs.group({ tabIds: ids, createProperties: { windowId: w.winId } });
            groupOf.set(key, gid);
          } catch (e) {
            console.warn("Helium Syncer: could not group tabs:", e);
          }
        }
      }
      for (const g of plan.groups || []) {
        const gid = groupOf.get(g.key);
        if (!gid) continue;
        const now = await chrome.tabGroups.get(gid).catch(() => null);
        if (!now) continue;
        const change = {};
        if ((now.title || "") !== (g.title || "")) change.title = g.title || "";
        if (now.color !== g.color) change.color = g.color;
        if (!!now.collapsed !== !!g.collapsed) change.collapsed = !!g.collapsed;
        if (Object.keys(change).length) await chrome.tabGroups.update(gid, change).catch(() => {});
      }
    }
  } catch (e) {
    console.warn("Helium Syncer: could not apply the tabs:", e);
  } finally {
    applyingTabs = false;
  }
  // Tell the app what we ended up with, once the new pages had a moment.
  scheduleTabs("applied");
}

// A tab this extension opened has settled once its page loaded and stayed
// put for a moment; after that, a new address is the person's doing.
async function settleOpened(tabId) {
  const opened = await openedTabs();
  if (!opened[tabId] || opened[tabId].settled) return;
  setTimeout(async () => {
    const t = await chrome.tabs.get(tabId).catch(() => null);
    const now = await openedTabs();
    if (!now[tabId]) return;
    if (!t) {
      delete now[tabId];
    } else if (t.status === "complete") {
      now[tabId].settled = t.url;
    }
    await saveOpened(now);
    scheduleTabs("settled");
  }, SETTLED_AFTER_MS);
}

// ---------------------------------------------------------------- the link

function connect() {
  if (socket && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) return;
  try {
    socket = new WebSocket(ADDRESS);
  } catch (e) {
    return retry();
  }

  socket.onopen = () => {
    backoffMs = 1000;
    socket.send(JSON.stringify({ type: "hello", version: VERSION }));
    sendTree("connected");
    // After the tree: the app learns which profile this is from the tree.
    scheduleTabs("connected");
    clearInterval(pingTimer);
    // Sending resets this worker's idle timer, which is what keeps it awake
    // while nothing else is happening.
    pingTimer = setInterval(() => {
      if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify({ type: "pong" }));
    }, PING_EVERY_MS);
  };

  socket.onmessage = async (event) => {
    let msg;
    try {
      msg = JSON.parse(event.data);
    } catch (e) {
      return;
    }
    if (msg.type === "apply" && Array.isArray(msg.nodes)) {
      const made = await apply(msg.nodes);
      // Say which id each new bookmark got, before the tree, so the app
      // knows them as the bookmarks it sent and not as new ones.
      if (made.size && socket && socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "created", version: VERSION, created: Object.fromEntries(made) }));
      }
      await sendTree("applied");      // tell the app what we ended up with
    } else if (msg.type === "applyTabs" && msg.apply) {
      await applyTabs(msg.apply);
    } else if (msg.type === "refused") {
      console.warn("Helium Syncer:", msg.reason || "the app refused this connection");
    }
  };

  socket.onclose = () => {
    clearInterval(pingTimer);
    socket = null;
    retry();
  };
  socket.onerror = () => {
    // onclose follows and handles it
  };
}

function retry() {
  // The app may be starting, or not installed yet. Back off, but never so far
  // that a change waits a long time once it is there.
  backoffMs = Math.min(backoffMs * 2, 30000);
  setTimeout(connect, backoffMs);
}

// ---------------------------------------------------------------- wiring

for (const event of ["onCreated", "onRemoved", "onChanged", "onMoved", "onChildrenReordered"]) {
  chrome.bookmarks[event].addListener(() => scheduleSend(event));
}

// Tabs: anything that changes which pages are open, and where. Title and
// loading changes are left out (nothing syncs them), except a finished load,
// which is when a page that moved by itself has arrived.
for (const event of ["onCreated", "onMoved", "onAttached", "onDetached", "onReplaced"]) {
  chrome.tabs[event].addListener(() => scheduleTabs(event));
}
chrome.tabs.onRemoved.addListener(async (tabId) => {
  const opened = await openedTabs();
  if (opened[tabId]) {
    delete opened[tabId];
    await saveOpened(opened);
  }
  scheduleTabs("onRemoved");
});
chrome.tabs.onUpdated.addListener((tabId, change) => {
  if (change.status === "complete") settleOpened(tabId);
  if (change.url !== undefined || change.pinned !== undefined || change.groupId !== undefined || change.status === "complete") {
    scheduleTabs("onUpdated");
  }
});
if (chrome.tabGroups) {
  for (const event of ["onCreated", "onUpdated", "onRemoved", "onMoved"]) {
    chrome.tabGroups[event].addListener(() => scheduleTabs("group " + event));
  }
}
chrome.windows.onCreated.addListener(() => scheduleTabs("window created"));
chrome.windows.onRemoved.addListener(() => scheduleTabs("window closed"));

// This worker is stopped when nothing happens for 30 seconds, which would
// drop the connection and leave changes from the other computer waiting. The
// alarm wakes it, and 30 s is the shortest Chromium allows.
chrome.alarms.create(WAKE_ALARM, { periodInMinutes: 0.5 });
chrome.alarms.onAlarm.addListener((a) => {
  if (a.name === WAKE_ALARM) connect();
});

// The alarm alone is not enough: on a Mac on battery the system can hold it
// back for minutes, and a worker that was stopped while the app was away
// then never comes back (seen on the real Mac). Switching tabs or windows
// wakes this worker too, so the link is back the moment Helium is used.
// Neither event needs a permission, and connect() does nothing when the
// link is already open.
chrome.tabs.onActivated.addListener(connect);
chrome.windows.onFocusChanged.addListener(connect);

chrome.runtime.onStartup.addListener(connect);
chrome.runtime.onInstalled.addListener(connect);
connect();
