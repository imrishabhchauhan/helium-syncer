// Helium Syncer, in the browser.
//
// Helium Syncer syncs your computers by reading and writing Helium's files,
// which it can only do while Helium is closed. Chromium keeps bookmarks in
// memory and rewrites the file when it quits, so anything written underneath
// a running browser is thrown away. This extension is the part that can
// change bookmarks while you are using them, because it asks Helium itself.
//
// It talks to the Helium Syncer app already running on this computer, over a
// WebSocket on a local port. Nothing leaves the computer from here: the app
// does the encryption and the talking to your other computer.

const ADDRESS = "ws://127.0.0.1:47811/live";
const VERSION = 1;
const SEND_AFTER_MS = 800;        // wait for a burst of edits to settle
const PING_EVERY_MS = 20000;      // keeps this worker awake (under the 30 s idle limit)
const WAKE_ALARM = "helium-syncer-wake";

let socket = null;
let sendTimer = null;
let pingTimer = null;
let applying = false;             // true while we are the ones changing bookmarks
let backoffMs = 1000;

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
