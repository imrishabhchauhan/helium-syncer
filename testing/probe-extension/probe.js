// Test-only helper. The test suite evaluates calls such as
//   probe.snapshot()
// in this service worker over the DevTools protocol.
self.probe = {
  async snapshot() {
    const windows = await chrome.windows.getAll({ populate: true });
    const groups = await chrome.tabGroups.query({});
    const tree = await chrome.bookmarks.getTree();
    return {
      windows: windows.map(w => ({
        id: w.id, type: w.type, focused: w.focused,
        tabs: w.tabs.map(t => ({ url: t.url || t.pendingUrl, title: t.title, pinned: t.pinned, groupId: t.groupId, index: t.index, active: t.active })),
      })),
      groups: groups.map(g => ({ id: g.id, title: g.title, color: g.color, collapsed: g.collapsed, windowId: g.windowId })),
      bookmarks: tree,
    };
  },
  async openTabs(urls, { pinned = false, group = null } = {}) {
    const ids = [];
    for (const url of urls) ids.push((await chrome.tabs.create({ url, pinned, active: false })).id);
    if (group) {
      const gid = await chrome.tabs.group({ tabIds: ids });
      await chrome.tabGroups.update(gid, { title: group.title, color: group.color });
    }
    return ids;
  },
  async addBookmark(parentTitle, title, url) {
    const [bar] = (await chrome.bookmarks.getTree())[0].children;
    let parent = bar;
    if (parentTitle) {
      const found = (await chrome.bookmarks.search({ title: parentTitle })).find(b => !b.url);
      parent = found || await chrome.bookmarks.create({ parentId: bar.id, title: parentTitle });
    }
    return chrome.bookmarks.create({ parentId: parent.id, title, url });
  },
  async history(maxResults = 1000) {
    return chrome.history.search({ text: '', startTime: 0, maxResults });
  },
  async extensions() {
    return (await chrome.management.getAll()).map(e => ({ id: e.id, name: e.name, version: e.version, enabled: e.enabled, installType: e.installType }));
  },
};
