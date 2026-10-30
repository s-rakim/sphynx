// App shell and the left column: chat list, status, calls, and the panels
// that slide over it (new chat, new group, settings, starred, archived).
// Every device on the Sphynx network is a contact, listed under the chats.
import * as A from './app.js';
import { S, api, render } from './app.js';
import { kvDelete } from './api.js';
import { newMessageId, nativeCrypto } from './crypto.js';
import { net, address, secure, canNotify, needsHttps, deviceLine, reachable, copyText } from './sphynx.js';
import { h, icon, iconButton, avatar, toast, menu, dialog, confirmDialog, promptDialog, listTime, duration, reconcile, Slots, toggle } from './ui.js';
import { resizePhoto, kindFor } from './media.js';
import * as O from './overlays.js';
import { renderConversation, drafts, clearChat, deleteChat, exitGroup, exitLabel, attachDialog, canSend, followChannel, openCommunity } from './conversation.js';
import { renderDetails, starredRow, inviteDialog, addMembers, deactivateCommunity } from './details.js';

const slots = new Slots();
let root;
let sidebarEl;
let mainEl;
let convEl;
let detailsEl;
let welcomeEl;
let chatSearchEl;
let panelSearchEl;
let panelKey = null;
let ng = null; // new group draft: { step, members, name, photo, photoUrl }

// ---------- small pieces ----------

function setView(view) {
  S.view = view;
  S.panel = null;
  render();
}

function openPanel(panel) {
  S.panel = panel;
  render();
}

function tickIcon(state) {
  const map = { pending: ['clock', ''], failed: ['info', 'danger'], read: ['checks', 'read'], delivered: ['checks', ''], sent: ['check', ''] };
  const [name, cls] = map[state] || map.sent;
  return h('span', { class: `tick ${cls}` }, icon(name));
}

function ringAvatar(user, seen, size = 'md') {
  return h('div', { class: `status-ring${seen ? ' seen' : ''}` }, avatar(user, size));
}

const prefs = (chat, body) => api.patch(`/api/chats/${encodeURIComponent(chat.id)}/prefs`, body).catch((e) => toast(e.message));

function chatMenu(chat, anchor) {
  const muted = chat.me.mutedUntil > Date.now();
  const unread = chat.unread || chat.me.markedUnread;
  menu(anchor, [
    { label: chat.me.archived ? 'Unarchive chat' : 'Archive chat', icon: 'archive', onClick: () => prefs(chat, { archived: !chat.me.archived }) },
    { label: muted ? 'Unmute notifications' : 'Mute notifications', icon: 'mute', onClick: () => (muted ? prefs(chat, { mutedUntil: 0 }) : O.muteDialog(chat)) },
    { label: chat.me.pinnedAt ? 'Unpin chat' : 'Pin chat', icon: 'pin', onClick: () => prefs(chat, { pinned: !chat.me.pinnedAt }) },
    {
      label: unread ? 'Mark as read' : 'Mark as unread',
      icon: 'unread',
      onClick: () => (unread
        ? api.post(`/api/chats/${encodeURIComponent(chat.id)}/receipts`, { delivered: chat.lastSeq, read: chat.lastSeq }).catch((e) => toast(e.message))
        : prefs(chat, { markedUnread: true })),
    },
    { label: 'Clear chat', icon: 'delete', onClick: () => clearChat(chat) },
    A.isGroupish(chat) && !chat.me.left
      ? { label: exitLabel(chat), icon: 'exit', danger: true, onClick: () => exitGroup(chat) }
      : { label: A.isGroupish(chat) ? `Delete ${A.noun(chat)}` : 'Delete chat', icon: 'delete', danger: true, onClick: () => deleteChat(chat) },
  ]);
}

function chatRowData(chat, title) {
  const last = chat.lastMessage;
  const draft = S.active !== chat.id ? (drafts.get(chat.id) || '').trim() : '';
  return {
    id: chat.id,
    title: title || A.chatTitle(chat),
    av: title ? { id: chat.id, name: title, group: 'channel' } : A.chatAvatar(chat),
    time: listTime(last?.createdAt || chat.createdAt),
    typing: A.typingText(chat.id),
    draft: draft.slice(0, 100),
    tick: last && last.kind === 'msg' && A.isMe(last.senderId) && !last.deletedAt ? A.tickState(last, chat) : null,
    prefix: last && A.showsSender(chat) && last.kind === 'msg' && !last.deletedAt && !A.isMe(last.senderId) ? `${A.nameOf(last.senderId)}: ` : '',
    preview: last ? A.previewText(last) : A.isGroupish(chat) ? 'No messages yet' : '',
    unread: chat.unread,
    marked: chat.me.markedUnread,
    muted: chat.me.mutedUntil > Date.now(),
    pinned: !!chat.me.pinnedAt,
    selected: S.active === chat.id,
  };
}

// title replaces the chat's own name (a community's "Announcements" row).
function chatRow(chat, title) {
  if (typeof title !== 'string') title = undefined; // also used as a map() callback
  const d = chatRowData(chat, title);
  return slots.get(`chat:${chat.id}${title ? ':alt' : ''}`, JSON.stringify(d), () => {
    let btn;
    let preview;
    if (d.typing) preview = h('span', { class: 'typing ellipsis' }, d.typing);
    else if (d.draft) preview = [h('span', { class: 'draft' }, 'Draft: '), h('span', { class: 'ellipsis' }, d.draft)];
    else preview = [d.tick ? tickIcon(d.tick) : null, h('span', { class: 'ellipsis' }, d.prefix + d.preview)];
    return h(
      'div',
      {
        class: `row${d.selected ? ' selected' : ''}`,
        onclick: () => {
          S.panel = null;
          A.openChat(chat.id);
        },
        oncontextmenu: (e) => {
          e.preventDefault();
          chatMenu(S.chats.get(chat.id) || chat, { x: e.clientX, y: e.clientY });
        },
      },
      avatar(d.av, 'md'),
      h(
        'div',
        { class: 'body' },
        h('div', { class: 'line' }, h('div', { class: 'name ellipsis grow' }, d.title), h('span', { class: `time${(d.unread || d.marked) && !d.muted ? ' unread' : ''}` }, d.time)),
        h(
          'div',
          { class: 'line' },
          h('div', { class: 'preview grow' }, preview),
          h(
            'span',
            { class: 'meta-icons' },
            d.muted ? icon('mute') : null,
            d.pinned ? icon('pin') : null,
            d.unread ? h('span', { class: `badge${d.muted ? ' muted' : ''}` }, String(d.unread)) : d.marked ? h('span', { class: `badge dot${d.muted ? ' muted' : ''}` }) : null,
            (btn = h('button', { type: 'button', class: 'row-menu', 'aria-label': 'Chat options', onclick: (e) => { e.stopPropagation(); chatMenu(S.chats.get(chat.id) || chat, btn); } }, icon('down'))),
          ),
        ),
      ),
    );
  });
}

function userRow(u, onClick, sub) {
  const dim = !A.isMe(u.id) && !reachable(u);
  return h(
    'div',
    { class: `row${dim ? ' dim' : ''}`, onclick: onClick },
    avatar(u, 'md'),
    h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, A.isMe(u.id) ? `${u.name} (You)` : u.name), h('div', { class: 'preview ellipsis' }, sub ?? (dim ? deviceLine(u) : u.about || deviceLine(u)))),
  );
}

const matches = (u, q) => [u.name, u.device, u.os, u.owner].some((x) => typeof x === 'string' && x.toLowerCase().includes(q));

// Devices on the network you have no chat with yet, by name.
function devicesWithoutChat() {
  const withChat = new Set([...S.chats.values()].filter((c) => c.type === 'direct').map((c) => A.otherMember(c)));
  return [...S.users.values()]
    .filter((u) => !A.isMe(u.id) && !withChat.has(u.id) && u.onNetwork !== false)
    .sort((a, b) => reachable(b) - reachable(a) || a.name.localeCompare(b.name));
}

function deviceRow(u) {
  const sig = JSON.stringify([u.name, u.device, u.os, u.owner, u.joined, u.onNetwork, u.avatar, u.about]);
  return slots.get(`device:${u.id}`, sig, () => userRow(u, () => openDevice(u.id)));
}

// How to get a device that hasn't opened the chat yet onto it.
function deviceHelp(u) {
  dialog({
    title: u.name,
    content: [
      h('p', null, u.onNetwork === false
        ? `${u.name} is no longer on your Sphynx network, so it can't get new messages.`
        : `${u.name} hasn't opened Sphynx Chat yet. Messages are encrypted for each device's own keys, which it makes the first time it opens the chat.`),
      u.onNetwork === false ? null : h('p', null, 'On that device, open this address in a browser while it is connected to Sphynx:'),
      u.onNetwork === false ? null : h('div', { class: 'invite-link' }, address()),
    ],
    actions: [
      u.onNetwork === false ? null : { label: 'Copy address', onClick: () => copyText(address(), 'Address copied', toast) },
      { label: 'OK', kind: 'primary', onClick: (close) => close() },
    ].filter(Boolean),
  });
}

function openDevice(userId) {
  const u = S.users.get(userId);
  if (u && !A.isMe(u.id) && !reachable(u)) {
    const chat = [...S.chats.values()].find((c) => c.type === 'direct' && A.otherMember(c) === u.id);
    if (chat) {
      S.panel = null;
      A.openChat(chat.id);
    } else deviceHelp(u);
    return;
  }
  startDirect(userId);
}

function addDeviceDialog() {
  dialog({
    title: 'Chat with another device',
    content: [
      h('p', null, 'Every device on your Sphynx network shows up here as its own contact. To add one, connect it to Sphynx with the Tailscale app (the Sphynx admin page shows how, under Add device).'),
      h('p', null, 'Then open this address in its browser:'),
      h('div', { class: 'invite-link' }, address()),
    ],
    actions: [
      { label: 'Copy address', onClick: () => copyText(address(), 'Address copied', toast) },
      { label: 'OK', kind: 'primary', onClick: (close) => close() },
    ],
  });
}

function sortChats(list) {
  const at = (c) => c.lastMessage?.createdAt || c.createdAt;
  return list.sort((a, b) => (b.me.pinnedAt || 0) - (a.me.pinnedAt || 0) || at(b) - at(a));
}

async function startDirect(userId) {
  try {
    S.panel = null;
    await A.openDirect(userId);
  } catch (err) {
    toast(err.message);
  }
}

// ---------- chats tab ----------

function searchResults(q) {
  const nodes = [];
  const chats = sortChats([...S.chats.values()].filter((c) => A.chatTitle(c).toLowerCase().includes(q)));
  if (chats.length) nodes.push(h('div', { class: 'list-section' }, 'Chats'), chats.map(chatRow));
  const direct = new Set(chats.filter((c) => c.type === 'direct').map((c) => A.otherMember(c)));
  const people = [...S.users.values()].filter((u) => !direct.has(u.id) && u.onNetwork !== false && matches(u, q));
  if (people.length) nodes.push(h('div', { class: 'list-section' }, 'Devices'), people.map((u) => userRow(u, () => openDevice(u.id))));
  const hits = [];
  for (const [chatId, b] of S.msgs) {
    if (!S.chats.has(chatId)) continue;
    for (const m of b.list) {
      const c = A.contentOf(m)?.ok;
      if (m.kind !== 'msg' || !c) continue;
      const text = [c.text, c.caption, c.file?.name, c.question].filter((x) => typeof x === 'string').join(' ');
      if (text.toLowerCase().includes(q)) hits.push(m);
    }
  }
  if (hits.length) {
    hits.sort((a, b) => b.createdAt - a.createdAt);
    nodes.push(h('div', { class: 'list-section' }, 'Messages'), hits.slice(0, 100).map((m) => starredRow(m)));
  }
  if (!nodes.length) nodes.push(h('div', { class: 'empty' }, 'No chats, devices or messages found'));
  nodes.push(h('div', { class: 'empty small' }, 'Messages are decrypted on this device, so search covers the chats you have opened.'));
  return nodes;
}

function chatsTab() {
  if (!chatSearchEl) {
    chatSearchEl = h('input', { type: 'search', placeholder: 'Search or start a new chat', 'aria-label': 'Search chats' });
    chatSearchEl.addEventListener('input', () => {
      S.search = chatSearchEl.value;
      render();
    });
  }
  const all = [...S.chats.values()].filter((c) => !A.isChannel(c));
  const archived = all.filter((c) => c.me.archived);
  const unreadArchived = archived.filter((c) => c.unread || c.me.markedUnread).length;
  const filters = slots.get('filters', S.filter, () => h(
    'div',
    { class: 'filters' },
    [['all', 'All'], ['unread', 'Unread'], ['groups', 'Groups']].map(([k, label]) =>
      h('button', { type: 'button', class: `chip${S.filter === k ? ' active' : ''}`, onclick: () => { S.filter = k; render(); } }, label)),
  ));
  const showBanner = canNotify && Notification.permission !== 'denied' && !S.prefs.notifications && !S.prefs.bannerDismissed;
  // Browsers hold back calls, voice messages and notifications on http.
  const secureBanner = !secure && net.secureAddress && !S.prefs.secureBannerDismissed
    ? slots.get('secure-banner', net.secureAddress, () => h(
      'div',
      { class: 'banner' },
      icon('lock'),
      h(
        'div',
        { class: 'grow' },
        h('div', null, 'Calls, voice messages and notifications work on the secure address'),
        h('a', { class: 'link', href: net.secureAddress }, `Open ${net.secureAddress}`),
        h('div', { class: 'muted small' }, 'This browser links to your chats again there, with new keys.'),
      ),
      iconButton('close', 'Dismiss', () => A.savePrefs({ secureBannerDismissed: true })),
    ))
    : null;
  const banner = showBanner
    ? slots.get('banner', '1', () => h(
      'div',
      { class: 'banner' },
      icon('bell'),
      h('div', { class: 'grow' }, h('div', null, 'Get notified of new messages'), h('button', { type: 'button', class: 'link', onclick: enableNotifications }, 'Turn on desktop notifications')),
      iconButton('close', 'Dismiss', () => A.savePrefs({ bannerDismissed: true })),
    ))
    : null;
  const offline = !S.online ? slots.get('offline', '1', () => h('div', { class: 'banner warn' }, icon('info'), h('div', null, "Connecting to Sphynx Chat… Messages you send now go out when you're back online."))) : null;
  const linked = S.prefs.securityAlerts?.devices || [];
  const linkedBanner = linked.length ? slots.get('linked', JSON.stringify(linked), () => h(
    'div',
    { class: 'banner warn' },
    icon('shield'),
    h(
      'div',
      { class: 'grow' },
      h('div', null, `New browser linked to this device: ${linked.map((d) => d.name).join(', ')}`),
      h('div', { class: 'muted small' }, "If this wasn't you, log it out now. Anyone who can use this device on your Sphynx network can link a browser."),
      h('button', { type: 'button', class: 'link', onclick: () => openPanel('devices') }, 'Review linked browsers'),
    ),
    iconButton('close', 'Dismiss', () => A.dismissSecurityAlerts({ devices: true })),
  )) : null;

  let list;
  const q = S.search.trim().toLowerCase();
  if (q) list = searchResults(q);
  else {
    const others = devicesWithoutChat();
    const shown = sortChats(all.filter((c) => {
      if (c.me.archived) return false;
      if (S.filter === 'unread') return c.unread || c.me.markedUnread || S.active === c.id;
      if (S.filter === 'groups') return c.type === 'group' || c.type === 'community';
      return true;
    }));
    list = [
      archived.length && S.filter === 'all'
        ? slots.get('archived-row', `${archived.length}:${unreadArchived}`, () => h(
          'div',
          { class: 'row short', onclick: () => openPanel('archived') },
          h('div', { class: 'row-icon' }, icon('archive')),
          h('div', { class: 'body' }, h('div', { class: 'line' }, h('div', { class: 'grow' }, 'Archived'), unreadArchived ? h('span', { class: 'time unread' }, String(unreadArchived)) : h('span', { class: 'time' }, String(archived.length)))),
        ))
        : null,
      shown.map(chatRow),
      !shown.length
        ? h('div', { class: 'empty' }, S.filter === 'unread' ? 'No unread chats' : S.filter === 'groups' ? 'No groups yet' : others.length ? 'No chats yet. Pick a device below to start one.' : 'No chats yet. Start one with the new chat button.')
        : null,
      S.filter === 'all' && others.length ? [h('div', { class: 'list-section' }, 'Other devices on your network'), others.map(deviceRow)] : null,
      shown.length || others.length ? h('div', { class: 'empty small' }, icon('lock'), ' Your personal messages are end-to-end encrypted') : null,
    ];
  }
  return [
    h('div', { class: 'search' }, icon('search'), chatSearchEl),
    q ? null : filters,
    offline,
    linkedBanner,
    secureBanner,
    banner,
    h('div', { class: 'list' }, list),
    h('button', { type: 'button', class: 'fab', 'aria-label': 'New chat', title: 'New chat', onclick: () => openPanel('new-chat') }, icon('chat')),
  ];
}

function enableNotifications() {
  if (!canNotify) return toast(secure ? "This browser can't show notifications" : needsHttps('notifications'));
  Notification.requestPermission().then((p) => {
    A.savePrefs({ notifications: p === 'granted' });
    if (p !== 'granted') toast('Notifications are blocked in your browser settings');
  });
}

// ---------- status tab ----------

function pickStatusMedia() {
  const input = h('input', { type: 'file', accept: 'image/*,video/*', class: 'hidden' });
  input.addEventListener('change', () => {
    const file = input.files[0];
    input.remove();
    if (file && !O.fileTooBig(file)) O.mediaStatusDialog(file);
  });
  document.body.append(input);
  input.click();
}

function statusSection() {
  const groups = O.statusGroups();
  const mine = groups.get(S.me.id) || [];
  const others = [...groups.entries()].filter(([u]) => !A.isMe(u));
  const recent = others.filter(([, l]) => l.some((s) => !s.seen));
  const viewed = others.filter(([, l]) => l.every((s) => s.seen));
  const personRow = ([uid, l]) => {
    const u = S.users.get(uid) || { id: uid, name: A.nameOf(uid) };
    return h(
      'div',
      { class: 'row', onclick: () => O.openStatus(uid) },
      ringAvatar(u, l.every((s) => s.seen)),
      h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, u.name), h('div', { class: 'preview' }, `${l.length > 1 ? `${l.length} updates · ` : ''}${listTime(l.at(-1).createdAt)}`)),
    );
  };
  return [
    h('div', { class: 'list-section' }, 'Status'),
    h(
      'div',
      { class: 'row', onclick: () => (mine.length ? O.openStatus(S.me.id) : O.textStatusDialog()) },
      mine.length ? ringAvatar(S.me, true) : avatar(S.me, 'md'),
      h('div', { class: 'body' }, h('div', { class: 'name' }, 'My status'), h('div', { class: 'preview' }, mine.length ? `${mine.length} update${mine.length === 1 ? '' : 's'} · ${listTime(mine.at(-1).createdAt)}` : 'Click to add a status update')),
      iconButton('camera', 'Photo or video status', (e) => { e.stopPropagation(); pickStatusMedia(); }),
      iconButton('edit', 'Text status', (e) => { e.stopPropagation(); O.textStatusDialog(); }),
    ),
    recent.length ? [h('div', { class: 'list-section sub' }, 'Recent updates'), recent.map(personRow)] : null,
    viewed.length ? [h('div', { class: 'list-section sub' }, 'Viewed updates'), viewed.map(personRow)] : null,
    !others.length ? h('div', { class: 'empty small' }, 'No updates from others yet. Status updates disappear after 24 hours.') : null,
    h('div', { class: 'empty small' }, icon('lock'), ' Your status updates are end-to-end encrypted'),
  ];
}

function action(iconName, label, onClick, sub) {
  return h('div', { class: 'row short', onclick: onClick }, h('div', { class: 'row-icon accent' }, icon(iconName)), h('div', { class: 'body' }, h('div', null, label), sub ? h('div', { class: 'preview' }, sub) : null));
}

function channelsSection() {
  const followed = sortChats([...S.chats.values()].filter((c) => A.isChannel(c) && !c.me.left));
  return [
    h('div', { class: 'list-section' }, 'Channels'),
    followed.map(chatRow),
    !followed.length ? h('div', { class: 'empty small' }, 'Follow channels to get updates on topics that matter to you, from the devices on your network.') : null,
    action('search', 'Find channels', () => openPanel('channels')),
    action('add', 'Create channel', newChannel),
  ];
}

function updatesTab() {
  return [h('div', { class: 'list' }, statusSection(), channelsSection())];
}

// ---------- channels ----------

function newChannel() {
  const name = h('input', { maxlength: 100, placeholder: 'Channel name' });
  const description = h('textarea', { rows: 3, maxlength: 2048, placeholder: 'Describe your channel (optional)' });
  dialog({
    title: 'New channel',
    content: [
      h('label', { class: 'field' }, name),
      h('label', { class: 'field' }, description),
      h('p', { class: 'muted small' }, icon('info'), " Any device on your Sphynx network can find and follow your channel. Like WhatsApp channels, posts aren't end-to-end encrypted, and followers can't see each other."),
    ],
    actions: [
      { label: 'Cancel', onClick: (close) => close() },
      {
        label: 'Create channel',
        kind: 'primary',
        onClick: async (close) => {
          if (!name.value.trim()) return toast('Give the channel a name');
          try {
            const { chat } = await api.post('/api/chats/channel', { name: name.value.trim(), description: description.value });
            S.chats.set(chat.id, chat);
            close();
            S.panel = null;
            A.openChat(chat.id);
          } catch (err) {
            toast(err.message);
          }
        },
      },
    ],
  });
}

let directory = null; // { list } for the channel directory panel

function channelsPanel() {
  if (!directory) {
    directory = { list: null };
    api.get('/api/channels').then(({ channels }) => {
      if (directory) directory.list = channels;
      render();
    }).catch((e) => toast(e.message));
  }
  const q = panelSearchEl?.value.trim().toLowerCase() || '';
  const list = (directory.list || []).filter((c) => !q || c.name.toLowerCase().includes(q) || (c.description || '').toLowerCase().includes(q));
  const open = async (c) => {
    const chat = c.following ? S.chats.get(c.id) || (await A.chatOf(c.id)) : await followChannel(c.id, true);
    if (!chat) return;
    S.panel = null;
    A.openChat(chat.id);
  };
  return panelShell('Find channels', [
    panelSearch('Search channels'),
    action('add', 'Create channel', newChannel),
    directory.list === null ? h('div', { class: 'empty' }, 'Loading channels…') : null,
    directory.list && !list.length ? h('div', { class: 'empty' }, q ? 'No channels found' : 'No device on your network has made a channel yet.') : null,
    list.map((c) => h(
      'div',
      { class: 'row', onclick: () => open(c) },
      avatar({ id: c.id, name: c.name, src: c.avatar, group: 'channel' }, 'md'),
      h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, c.name), h('div', { class: 'preview ellipsis' }, `${c.followers} follower${c.followers === 1 ? '' : 's'}${c.description ? ` · ${c.description}` : ''}`)),
      c.following ? h('span', { class: 'tag' }, 'Following') : h('button', { type: 'button', class: 'btn primary small', onclick: (e) => { e.stopPropagation(); open(c); } }, 'Follow'),
    )),
  ]);
}

// ---------- communities ----------

function newCommunity() {
  const name = h('input', { maxlength: 100, placeholder: 'Community name' });
  const description = h('textarea', { rows: 3, maxlength: 2048, placeholder: "What's this community about?" });
  dialog({
    title: 'New community',
    content: [
      h('p', { class: 'muted' }, 'Bring related groups together under one community, with an announcements group that only admins post in.'),
      h('label', { class: 'field' }, name),
      h('label', { class: 'field' }, description),
    ],
    actions: [
      { label: 'Cancel', onClick: (close) => close() },
      {
        label: 'Create community',
        kind: 'primary',
        onClick: async (close) => {
          if (!name.value.trim()) return toast('Give the community a name');
          try {
            const { chat } = await api.post('/api/chats/community', { name: name.value.trim(), description: description.value });
            S.chats.set(chat.id, chat);
            close();
            openCommunity(chat.id);
          } catch (err) {
            toast(err.message);
          }
        },
      },
    ],
  });
}

function communitiesTab() {
  const communities = [...S.chats.values()].filter((c) => c.type === 'community' && !c.me.left).sort((a, b) => a.name.localeCompare(b.name));
  return [
    h(
      'div',
      { class: 'list' },
      action('community', 'New community', newCommunity),
      communities.map((c) => {
        const groups = sortChats([...S.chats.values()].filter((g) => g.communityId === c.id && !g.me.left));
        return h(
          'div',
          { class: 'community-block' },
          h('div', { class: 'row', onclick: () => openCommunity(c.id) }, avatar(A.chatAvatar(c), 'md'), h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, c.name), h('div', { class: 'preview' }, `${(c.groups || []).length} group${(c.groups || []).length === 1 ? '' : 's'}`))),
          chatRow(c, 'Announcements'),
          groups.slice(0, 3).map((g) => chatRow(g)),
          h('div', { class: 'row short', onclick: () => openCommunity(c.id) }, h('div', { class: 'row-icon' }, icon('forward')), h('div', { class: 'body' }, 'View all')),
        );
      }),
      !communities.length ? h('div', { class: 'empty' }, "Communities bring members together in topic-based groups, with announcements from admins. You're not in any community yet.") : null,
    ),
  ];
}

function communityPanel() {
  const c = S.chats.get(S.community);
  if (!c || c.type !== 'community') return panelShell('Community', h('div', { class: 'empty' }, "This community isn't available."));
  const admin = c.me.role === 'admin' && !c.me.left;
  const groups = c.groups || [];
  const joined = groups.filter((g) => g.joined);
  const others = groups.filter((g) => !g.joined);
  const members = c.members.filter((m) => !m.left).length;
  const join = async (g) => {
    try {
      const { chat } = await api.post(`/api/communities/${encodeURIComponent(c.id)}/groups/${encodeURIComponent(g.id)}/join`);
      S.chats.set(chat.id, chat);
      S.panel = null;
      A.openChat(chat.id);
    } catch (err) {
      toast(err.message);
    }
  };
  const groupRow = (g, right) => {
    let more;
    return h(
      'div',
      { class: 'row', onclick: () => (g.joined ? (S.panel = null, A.openChat(g.id)) : join(g)) },
      avatar({ id: g.id, name: g.name, src: g.avatar, group: true }, 'md'),
      h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, g.name), h('div', { class: 'preview ellipsis' }, `${g.members} member${g.members === 1 ? '' : 's'}${g.description ? ` · ${g.description}` : ''}`)),
      right,
      admin ? (more = h('button', { type: 'button', class: 'row-menu', 'aria-label': 'Group options', onclick: (e) => {
        e.stopPropagation();
        menu(more, [{ label: 'Remove from community', icon: 'exit', danger: true, onClick: () => removeGroup(c, g) }]);
      } }, icon('down'))) : null,
    );
  };
  return panelShell(c.name, [
    h(
      'div',
      { class: 'card center' },
      avatar(A.chatAvatar(c), 'lg'),
      h('h2', null, c.name),
      h('h3', null, `Community · ${members} member${members === 1 ? '' : 's'}`),
      c.description ? h('p', { class: 'about' }, c.description) : null,
    ),
    chatRow(c, 'Announcements'),
    joined.length ? [h('div', { class: 'list-section' }, "Groups you're in"), joined.map((g) => groupRow(g, null))] : null,
    others.length ? [h('div', { class: 'list-section' }, 'Groups you can join'), others.map((g) => groupRow(g, h('button', { type: 'button', class: 'btn plain small', onclick: (e) => { e.stopPropagation(); join(g); } }, 'Join')))] : null,
    !groups.length ? h('div', { class: 'empty small' }, admin ? 'Add a group to get started.' : 'No groups yet.') : null,
    admin ? [
      h('div', { class: 'list-section' }, 'Manage'),
      action('add', 'New group', () => newCommunityGroup(c)),
      action('group', 'Add existing groups', () => linkGroups(c)),
      action('person', 'Add members', () => addMembers(c)),
      action('link', 'Invite to community via link', () => inviteDialog(c)),
    ] : null,
    action('info', 'Community info', () => { S.panel = null; A.openChat(c.id); S.details = 'info'; render(); }),
    h(
      'div',
      { class: 'card flush' },
      admin ? settingsRow('delete', 'Deactivate community', 'Groups carry on without it', () => deactivateCommunity(c), true) : null,
      settingsRow('exit', 'Exit community', "You'll also leave its groups", () => exitGroup(c), true),
    ),
  ], null);
}

async function newCommunityGroup(c) {
  const name = await promptDialog('New group in the community', { placeholder: 'Group name', okLabel: 'Create', max: 100 });
  if (!name?.trim()) return;
  try {
    const { chat } = await api.post(`/api/communities/${encodeURIComponent(c.id)}/groups`, { name: name.trim() });
    S.chats.set(chat.id, chat);
    S.panel = null;
    A.openChat(chat.id);
  } catch (err) {
    toast(err.message);
  }
}

// Groups you admin that aren't in a community yet.
function linkGroups(c) {
  const candidates = [...S.chats.values()].filter((g) => g.type === 'group' && !g.communityId && !g.me.left && g.me.role === 'admin');
  if (!candidates.length) return toast("You don't admin any groups that could be added. Only group admins can add their group to a community.");
  const boxes = new Map();
  dialog({
    title: 'Add existing groups',
    flush: true,
    content: h('div', { class: 'pick-list' }, candidates.map((g) => {
      const box = h('input', { type: 'checkbox', class: 'row-check' });
      boxes.set(g.id, box);
      return h('label', { class: 'row' }, avatar(A.chatAvatar(g), 'md'), h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, g.name), h('div', { class: 'preview' }, `${g.members.filter((m) => !m.left).length} members`)), box);
    })),
    actions: [
      { label: 'Cancel', onClick: (close) => close() },
      {
        label: 'Add',
        kind: 'primary',
        onClick: async (close) => {
          close();
          for (const [groupId, box] of boxes) {
            if (!box.checked) continue;
            await api.post(`/api/communities/${encodeURIComponent(c.id)}/groups`, { groupId }).catch((e) => toast(e.message));
          }
        },
      },
    ],
  });
}

async function removeGroup(c, g) {
  if (!(await confirmDialog(`Remove "${g.name}"?`, 'The group carries on as a normal group. Its members stay in the community.', 'Remove', 'danger'))) return;
  api.del(`/api/communities/${encodeURIComponent(c.id)}/groups/${encodeURIComponent(g.id)}`).catch((e) => toast(e.message));
}

// ---------- calls tab ----------

async function newCall(video) {
  const ids = await O.pickUsers({ title: video ? 'New video call' : 'New voice call', multiple: false });
  if (!ids?.length) return;
  try {
    const { chat } = await api.post('/api/chats/direct', { userId: ids[0] });
    S.chats.set(chat.id, chat);
    await O.startCall(chat, video);
  } catch (err) {
    toast(err.message);
  }
}

function callsTab() {
  const rows = S.calls
    .map((c) => {
      const chat = S.chats.get(c.chatId);
      if (!chat) return null;
      const outgoing = A.isMe(c.callerId);
      const missed = !c.answeredAt && !outgoing;
      const length = c.answeredAt && c.endedAt ? ` · ${duration((c.endedAt - c.answeredAt) / 1000)}` : '';
      return h(
        'div',
        { class: 'row', onclick: () => A.openChat(chat.id) },
        avatar(A.chatAvatar(chat), 'md'),
        h(
          'div',
          { class: 'body' },
          h('div', { class: `name ellipsis${missed ? ' danger' : ''}` }, A.chatTitle(chat)),
          h('div', { class: 'preview' }, h('span', { class: `call-dir${missed ? ' danger' : ''}` }, icon(outgoing ? 'callOut' : 'callIn')), `${missed ? 'Missed · ' : !c.answeredAt ? 'No answer · ' : ''}${listTime(c.startedAt)}${length}`),
        ),
        !chat.me.left ? iconButton(c.video ? 'video' : 'phone', c.video ? 'Video call' : 'Voice call', (e) => { e.stopPropagation(); O.startCall(chat, c.video); }) : null,
      );
    })
    .filter(Boolean);
  return [
    h(
      'div',
      { class: 'list' },
      h('div', { class: 'row short', onclick: () => newCall(false) }, h('div', { class: 'row-icon accent' }, icon('phone')), h('div', { class: 'body' }, 'New voice call')),
      h('div', { class: 'row short', onclick: () => newCall(true) }, h('div', { class: 'row-icon accent' }, icon('video')), h('div', { class: 'body' }, 'New video call')),
      h('div', { class: 'list-section' }, 'Recent'),
      rows.length ? rows : h('div', { class: 'empty' }, 'No calls yet'),
      h('div', { class: 'empty small' }, icon('lock'), secure ? ' Calls go device to device across your Sphynx network and are end-to-end encrypted' : ` ${needsHttps('calls')}`),
    ),
  ];
}

// ---------- main sidebar ----------

async function logout() {
  if (!(await confirmDialog('Log out this browser?', 'This browser will be unlinked and its encryption key deleted, so it can no longer read your messages. Other browsers and devices are not affected.', 'Log out', 'danger'))) return;
  try {
    await api.del(`/api/devices/${encodeURIComponent(S.device.id)}`);
  } catch {
    // already gone
  }
  await kvDelete(`device:${S.me.id}`);
  location.reload();
}

function topbar() {
  const unseen = [...S.statuses.values()].some((s) => !s.seen && !A.isMe(s.userId));
  const sig = JSON.stringify([S.me, S.online, unseen]);
  return slots.get('topbar', sig, () => {
    let more;
    return h(
      'header',
      { class: 'topbar' },
      h('button', { type: 'button', class: 'avatar-btn', 'aria-label': 'Profile', onclick: () => openPanel('profile') }, avatar(S.me)),
      h('div', { class: 'title' }, h('div', { class: 'name' }, 'Sphynx Chat'), h('div', { class: 'sub ellipsis' }, S.online ? S.me.name : 'Connecting…')),
      iconButton('group', 'New group', () => startNewGroup()),
      iconButton('chat', 'New chat', () => openPanel('new-chat')),
      (more = iconButton('more', 'Menu', () => menu(more, [
        { label: 'New group', icon: 'group', onClick: () => startNewGroup() },
        { label: 'New community', icon: 'community', onClick: newCommunity },
        { label: 'New channel', icon: 'channel', onClick: newChannel },
        { label: 'Broadcast lists', icon: 'broadcast', onClick: () => openPanel('broadcasts') },
        { label: 'Archived', icon: 'archive', onClick: () => openPanel('archived') },
        { label: 'Starred messages', icon: 'star', onClick: () => openPanel('starred') },
        { label: 'Linked browsers', icon: 'devices', onClick: () => openPanel('devices') },
        { label: 'Settings', icon: 'settings', onClick: () => openPanel('settings') },
        { label: 'Log out', icon: 'exit', danger: true, onClick: logout },
      ]))),
    );
  });
}

function tabs() {
  let unread = 0;
  let channelUnread = false;
  for (const c of S.chats.values()) {
    const has = !c.me.archived && (c.unread || c.me.markedUnread);
    if (has && A.isChannel(c)) channelUnread = true;
    else if (has) unread++;
  }
  const unseen = channelUnread || [...S.statuses.values()].some((s) => !s.seen && !A.isMe(s.userId));
  const missed = S.calls.filter((c) => !c.answeredAt && !A.isMe(c.callerId) && c.startedAt > (S.prefs.callsSeenAt || 0)).length;
  return slots.get('tabs', JSON.stringify([S.view, unread, unseen, missed]), () => {
    const tab = (view, label, badge) => h('button', {
      type: 'button',
      class: `tab${S.view === view ? ' active' : ''}`,
      onclick: () => {
        if (view === 'calls') A.savePrefs({ callsSeenAt: Date.now() });
        setView(view);
      },
    }, label, badge);
    return h(
      'nav',
      { class: 'tabs' },
      tab('chats', 'Chats', unread ? h('span', { class: 'badge' }, String(unread)) : null),
      tab('status', 'Updates', unseen ? h('span', { class: 'badge dot' }) : null),
      tab('communities', 'Communities'),
      tab('calls', 'Calls', missed ? h('span', { class: 'badge' }, String(missed)) : null),
    );
  });
}

// ---------- panels ----------

function panelShell(title, content, back = null, extra = null) {
  return [
    h('header', { class: 'topbar panel-head' }, iconButton('back', 'Back', () => openPanel(back)), h('div', { class: 'title' }, h('div', { class: 'name' }, title)), extra),
    h('div', { class: 'list' }, content),
  ];
}

function panelSearch(placeholder) {
  if (!panelSearchEl) {
    panelSearchEl = h('input', { type: 'search', placeholder, 'aria-label': placeholder });
    panelSearchEl.addEventListener('input', () => render());
    setTimeout(() => panelSearchEl?.focus(), 0);
  }
  return h('div', { class: 'search' }, icon('search'), panelSearchEl);
}

// Devices to pick from; onlyReachable leaves out the ones that can't get
// messages yet.
function matchUsers(q, exclude = new Set(), onlyReachable = false) {
  return [...S.users.values()]
    .filter((u) => !A.isMe(u.id) && !exclude.has(u.id) && u.onNetwork !== false && (!onlyReachable || reachable(u)) && (!q || matches(u, q)))
    .sort((a, b) => reachable(b) - reachable(a) || a.name.localeCompare(b.name));
}

function newChatPanel() {
  const q = panelSearchEl?.value.trim().toLowerCase() || '';
  const users = matchUsers(q);
  return panelShell('New chat', [
    panelSearch('Search devices'),
    !q ? [
      action('group', 'New group', () => startNewGroup()),
      action('broadcast', 'New broadcast', () => newBroadcast()),
      action('link', 'Chat with another device', addDeviceDialog),
      userRow(S.me, () => startDirect(S.me.id), 'Message yourself'),
    ] : null,
    h('div', { class: 'list-section' }, 'Devices on your network'),
    users.map((u) => userRow(u, () => openDevice(u.id))),
    !users.length ? h('div', { class: 'empty' }, q ? 'No devices found' : 'No other devices on your Sphynx network yet. Add one in the Sphynx admin page, under Add device.') : null,
  ]);
}

function startNewGroup() {
  ng = { step: 1, members: new Set(), name: null, photo: null, photoUrl: null };
  openPanel('new-group');
}

function newGroupPanel() {
  if (!ng) ng = { step: 1, members: new Set(), name: null, photo: null, photoUrl: null };
  if (ng.step === 1) {
    const q = panelSearchEl?.value.trim().toLowerCase() || '';
    return [
      ...panelShell('Add group members', [
        h('div', { class: 'chips-sel pad' }, [...ng.members].map((id) => h('button', { type: 'button', class: 'chip', onclick: () => { ng.members.delete(id); render(); } }, A.nameOf(id), ' ×'))),
        panelSearch('Search devices'),
        matchUsers(q, new Set(), true).map((u) => h(
          'div',
          { class: 'row', onclick: () => { if (ng.members.has(u.id)) ng.members.delete(u.id); else ng.members.add(u.id); render(); } },
          avatar(u, 'md'),
          h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, u.name), h('div', { class: 'preview ellipsis' }, u.about || deviceLine(u))),
          h('input', { type: 'checkbox', checked: ng.members.has(u.id), tabindex: -1, class: 'row-check' }),
        )),
      ]),
      h('button', { type: 'button', class: 'fab', 'aria-label': 'Next', title: 'Next', onclick: () => { ng.step = 2; panelSearchEl = null; render(); } }, icon('forward')),
    ];
  }
  if (ng.name === null) {
    ng.name = h('input', { maxlength: 100, placeholder: 'Group name' });
    setTimeout(() => ng?.name?.focus(), 0);
  }
  let photoBtn;
  return [
    ...panelShell('New group', [
      h(
        'div',
        { class: 'card center' },
        (photoBtn = h('button', {
          type: 'button',
          class: 'avatar-btn',
          title: 'Add group icon',
          onclick: () => pickImage(async (file) => {
            ng.photo = await resizePhoto(file);
            if (ng.photoUrl) URL.revokeObjectURL(ng.photoUrl);
            ng.photoUrl = URL.createObjectURL(ng.photo);
            render();
          }),
        }, ng.photoUrl ? avatar({ id: 'new', name: '', src: ng.photoUrl }, 'lg') : h('div', { class: 'avatar lg placeholder' }, icon('camera'), h('span', null, 'Add group icon')))),
        h('label', { class: 'field' }, ng.name),
        h('div', { class: 'muted' }, `${ng.members.size} member${ng.members.size === 1 ? '' : 's'} besides you: ${[...ng.members].map(A.nameOf).join(', ') || 'none yet'}`),
      ),
    ], null, null),
    h('button', { type: 'button', class: 'fab', 'aria-label': 'Create group', title: 'Create group', onclick: createGroup }, icon('check')),
  ];
}

async function createGroup() {
  const name = ng.name.value.trim();
  if (!name) return toast('Give the group a name');
  try {
    let avatarId;
    if (ng.photo) ({ id: avatarId } = await api.upload(ng.photo, { isPublic: true }));
    const { chat } = await api.post('/api/chats/group', { name, members: [...ng.members], avatar: avatarId });
    S.chats.set(chat.id, chat);
    if (ng.photoUrl) URL.revokeObjectURL(ng.photoUrl);
    ng = null;
    S.panel = null;
    A.openChat(chat.id);
  } catch (err) {
    toast(err.message);
  }
}

function pickImage(onPick) {
  const input = h('input', { type: 'file', accept: 'image/*', class: 'hidden' });
  input.addEventListener('change', () => {
    const file = input.files[0];
    input.remove();
    if (file) onPick(file).catch?.((e) => toast(e.message));
  });
  document.body.append(input);
  input.click();
}

function archivedPanel() {
  const list = sortChats([...S.chats.values()].filter((c) => c.me.archived));
  return panelShell('Archived', [
    h('div', { class: 'empty small' }, 'These chats stay archived when new messages arrive. Right-click a chat to unarchive it.'),
    list.map(chatRow),
    !list.length ? h('div', { class: 'empty' }, 'No archived chats') : null,
  ]);
}

function starredPanel() {
  if (S.starred === null) {
    S.starred = [];
    A.loadStarred().catch((e) => toast(e.message));
  }
  return panelShell('Starred messages', S.starred.length ? S.starred.map((m) => starredRow(m)) : h('div', { class: 'empty' }, 'Star messages to find them here later. Right-click a message and choose Star.'));
}

function settingsRow(iconName, label, sub, onClick, danger = false) {
  return h('button', { type: 'button', class: `menu-row${danger ? ' danger' : ''}`, onclick: onClick }, icon(iconName), h('div', { class: 'grow' }, h('div', { class: danger ? 'danger' : '' }, label), sub ? h('div', { class: 'sub' }, sub) : null));
}

function settingsPanel() {
  return panelShell('Settings', [
    h('div', { class: 'row tall', onclick: () => openPanel('profile') }, avatar(S.me, 'md'), h('div', { class: 'body' }, h('div', { class: 'name' }, S.me.name), h('div', { class: 'preview ellipsis' }, S.me.about || deviceLine(S.me)))),
    settingsRow('lock', 'Privacy', 'Last seen, read receipts, blocked contacts', () => openPanel('privacy')),
    settingsRow('chat', 'Chats', 'Theme, enter to send', () => openPanel('chat-settings')),
    settingsRow('bell', 'Notifications', S.prefs.notifications ? 'On' : 'Off', () => openPanel('notifications')),
    settingsRow('devices', 'Linked browsers', 'Where this device has Sphynx Chat open', () => openPanel('devices')),
    settingsRow('shield', 'Security', 'How Sphynx Chat protects your messages', () => openPanel('help')),
    settingsRow('exit', 'Log out', null, logout, true),
  ], null);
}

async function changeProfilePhoto(file) {
  const blob = await resizePhoto(file);
  const { id } = await api.upload(blob, { isPublic: true });
  await api.patch('/api/me', { avatar: id });
}

function profilePanel() {
  let photoBtn;
  const edit = async (field, title, max, multiline) => {
    const value = await promptDialog(title, { value: S.me[field] || '', max, multiline, okLabel: 'Save' });
    if (value === null) return;
    if (field === 'name' && !value.trim()) return toast("Your name can't be empty");
    try {
      const { user } = await api.patch('/api/me', { [field]: value.trim() });
      S.me = { ...S.me, ...user };
      S.users.set(user.id, { ...S.users.get(user.id), ...user });
      render();
    } catch (err) {
      toast(err.message);
    }
  };
  return panelShell('Profile', [
    h('div', { class: 'card center' }, (photoBtn = h('button', {
      type: 'button',
      class: 'avatar-btn',
      title: 'Change profile photo',
      onclick: () => menu(photoBtn, [
        { label: 'Upload photo', icon: 'image', onClick: () => pickImage((f) => changeProfilePhoto(f).catch((e) => toast(e.message))) },
        S.me.avatar && S.me.avatar.startsWith('/api/avatars/') ? { label: 'Remove photo', icon: 'delete', onClick: () => api.patch('/api/me', { avatar: null }).catch((e) => toast(e.message)) } : null,
      ]),
    }, avatar(S.me, 'lg')))),
    h('div', { class: 'card' }, h('h3', null, 'Your name'), h('div', { class: 'editable-line', onclick: () => edit('name', 'Your name', 60) }, h('span', { class: 'grow' }, S.me.name), icon('edit')), h('p', { class: 'muted small' }, "Every device on your Sphynx network sees this name. It starts out as this device's name on the network.")),
    h('div', { class: 'card' }, h('h3', null, 'About'), h('div', { class: 'editable-line', onclick: () => edit('about', 'About', 140, true) }, h('span', { class: 'grow' }, S.me.about || 'Hey there! I am using Sphynx Chat.'), icon('edit'))),
    h(
      'div',
      { class: 'card' },
      h('h3', null, 'This device'),
      h('div', null, S.me.device || S.me.name),
      [S.me.os, S.me.owner].some(Boolean) ? h('div', { class: 'muted small' }, [S.me.os, S.me.owner].filter(Boolean).join(' · ')) : null,
      h('p', { class: 'muted small' }, S.authMode === 'local'
        ? 'On the PC that runs Sphynx you chat as Sphynx home, after signing in to the Sphynx admin page.'
        : 'Sphynx Chat knows which device this is from its connection to your Sphynx network. There are no passwords.'),
    ),
  ], 'settings');
}

function privacyPanel() {
  const blocked = [...S.users.values()].filter((u) => u.blocked);
  const set = (body) => api.patch('/api/me', body).then(({ user }) => { S.me = { ...S.me, ...user }; render(); }).catch((e) => toast(e.message));
  return panelShell('Privacy', [
    h('div', { class: 'menu-row' }, icon('clock'), h('div', { class: 'grow' }, h('div', null, 'Last seen and online'), h('div', { class: 'sub' }, "If you hide yours, you won't see other people's either")), toggle(!S.me.hideLastSeen, (on) => set({ hideLastSeen: !on }), 'Share last seen')),
    h('div', { class: 'menu-row' }, icon('checks'), h('div', { class: 'grow' }, h('div', null, 'Read receipts'), h('div', { class: 'sub' }, "If you turn these off, you won't send or receive read receipts in direct chats. Groups always send them.")), toggle(S.me.readReceipts !== false, (on) => set({ readReceipts: on }), 'Read receipts')),
    h('div', { class: 'list-section' }, `Blocked contacts${blocked.length ? ` (${blocked.length})` : ''}`),
    blocked.map((u) => userRow(u, async () => {
      if (await confirmDialog(`Unblock ${u.name}?`, 'They will be able to message and call you again.', 'Unblock')) api.del(`/api/blocks/${encodeURIComponent(u.id)}`).catch((e) => toast(e.message));
    }, 'Click to unblock')),
    h('div', { class: 'row short', onclick: async () => {
      const ids = await O.pickUsers({ title: 'Block contact', multiple: false, exclude: blocked.map((u) => u.id), everyone: true });
      if (ids?.[0]) api.post('/api/blocks', { userId: ids[0] }).catch((e) => toast(e.message));
    } }, h('div', { class: 'row-icon' }, icon('block')), h('div', { class: 'body' }, 'Block a contact')),
  ], 'settings');
}

function chatSettingsPanel() {
  const theme = (value, label) => h('label', { class: 'radio-row pad' }, h('input', { type: 'radio', name: 'theme', checked: S.prefs.theme === value, onchange: () => A.savePrefs({ theme: value }) }), label);
  return panelShell('Chats', [
    h('div', { class: 'list-section' }, 'Theme'),
    theme('system', 'System default'),
    theme('light', 'Light'),
    theme('dark', 'Dark'),
    h('div', { class: 'list-section' }, 'Keyboard'),
    h('div', { class: 'menu-row' }, icon('send'), h('div', { class: 'grow' }, h('div', null, 'Enter is send'), h('div', { class: 'sub' }, 'Shift+Enter adds a new line')), toggle(S.prefs.enterToSend, (on) => A.savePrefs({ enterToSend: on }), 'Enter is send')),
  ], 'settings');
}

function notificationsPanel() {
  const supported = canNotify;
  const why = !secure ? needsHttps('notifications') : !supported ? "This browser can't show notifications" : Notification.permission === 'denied' ? 'Blocked in your browser settings' : 'Show new messages and calls while Sphynx Chat is in the background';
  return panelShell('Notifications', [
    h('div', { class: 'menu-row' }, icon('bell'), h('div', { class: 'grow' }, h('div', null, 'Desktop notifications'), h('div', { class: 'sub' }, why)), supported ? toggle(S.prefs.notifications && Notification.permission === 'granted', (on) => (on ? enableNotifications() : A.savePrefs({ notifications: false })), 'Desktop notifications') : null),
    h('div', { class: 'empty small' }, 'Notifications show the sender and a preview. They are generated on this device after decryption; the server never sees message text.'),
  ], 'settings');
}

let devicesCache = null;
function devicesPanel() {
  if (!devicesCache) {
    devicesCache = { list: null };
    api.get('/api/devices').then(({ devices }) => { devicesCache.list = devices; render(); }).catch((e) => toast(e.message));
  }
  const list = (devicesCache.list || []).filter((d) => !d.revokedAt);
  return panelShell('Linked browsers', [
    h('div', { class: 'card' }, h('p', null, `These are the browsers that have Sphynx Chat open on ${S.me.device || S.me.name}. Other devices are separate contacts: to chat from one, connect it to Sphynx and open this address on it:`), h('div', { class: 'invite-link' }, address()), h('button', { type: 'button', class: 'btn plain', onclick: () => copyText(address(), 'Address copied', toast) }, 'Copy address'), h('p', { class: 'muted small' }, 'Each browser gets its own encryption key. Messages sent before a browser was linked stay unreadable in it.')),
    h('div', { class: 'list-section' }, `${list.length} active browser${list.length === 1 ? '' : 's'}`),
    list.map((d) => h(
      'div',
      { class: 'row' },
      h('div', { class: 'row-icon' }, icon('devices')),
      h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, d.name, d.id === S.device.id ? ' (this browser)' : ''), h('div', { class: 'preview' }, `Linked ${listTime(d.createdAt)} · active ${listTime(d.lastActive)}`)),
      d.id !== S.device.id ? h('button', { type: 'button', class: 'btn plain small', onclick: async () => {
        if (!(await confirmDialog(`Log out ${d.name}?`, 'It will stop receiving messages and its key will no longer be used.', 'Log out', 'danger'))) return;
        await api.del(`/api/devices/${encodeURIComponent(d.id)}`).catch((e) => toast(e.message));
        devicesCache = null;
        render();
      } }, 'Log out') : null,
    )),
  ], null);
}

function helpPanel() {
  const p = (text) => h('p', null, text);
  return panelShell('Security', [
    h('div', { class: 'card' },
      p('Sphynx Chat runs on your own PC, as part of Sphynx, and only devices on your Sphynx network can reach it. Sphynx knows which device each connection comes from, so there are no passwords or phone numbers. Every device is its own contact.'),
      p('Every message, file, voice note and status update is encrypted on your device with keys that never leave it. The server stores ciphertext and the minimum it needs to deliver messages: who is in which chat, when messages were sent, and file sizes.'),
      p('Each chat has a security code you can compare with the other devices in it. If the codes match, nobody (including whoever runs the server) can read your messages.'),
      p('Calls connect device to device across your Sphynx network. Media is encrypted twice: by WebRTC and by WireGuard.'),
      nativeCrypto ? null : p("This browser opened the chat on a plain http address, where browsers don't offer their own encryption tools, so Sphynx Chat brings the same algorithms itself. Messages are just as encrypted, but this page could read its own private key, which the browser's tools prevent. The HTTPS address avoids that."),
      h('p', { class: 'muted small' }, `Signed in as ${S.me.device || S.me.name} in ${S.device.name}.`),
    ),
  ], 'settings');
}

// ---------- broadcast lists (stored on this device, like on a phone) ----------

const broadcasts = () => (Array.isArray(S.prefs.broadcasts) ? S.prefs.broadcasts : []);

async function newBroadcast() {
  const ids = await O.pickUsers({ title: 'New broadcast: pick recipients', okLabel: 'Next' });
  if (!ids?.length) return;
  const name = await promptDialog('Broadcast list name', { value: ids.map(A.nameOf).join(', ').slice(0, 60), okLabel: 'Create', max: 60 });
  if (name === null) return;
  A.savePrefs({ broadcasts: [...broadcasts(), { id: newMessageId(), name: name.trim() || 'Broadcast list', members: ids }] });
  openPanel('broadcasts');
}

function broadcastDialog(list) {
  const ta = h('textarea', { rows: 4, maxlength: 65536, placeholder: 'Type a message' });
  let files = [];
  const fileLine = h('div', { class: 'muted' });
  dialog({
    title: list.name,
    content: [
      h('p', { class: 'muted' }, `Sends a separate encrypted message to each of: ${list.members.map(A.nameOf).join(', ')}. Replies arrive in your chat with each device.`),
      h('label', { class: 'field' }, ta),
      fileLine,
      h('button', { type: 'button', class: 'btn plain', onclick: () => {
        const input = h('input', { type: 'file', multiple: true, class: 'hidden' });
        input.addEventListener('change', () => {
          files = [...input.files].filter((f) => !O.fileTooBig(f));
          fileLine.textContent = files.map((f) => f.name).join(', ');
          input.remove();
        });
        document.body.append(input);
        input.click();
      } }, icon('attach'), ' Attach files'),
    ],
    actions: [
      { label: 'Delete list', onClick: (close) => { close(); A.savePrefs({ broadcasts: broadcasts().filter((b) => b.id !== list.id) }); } },
      {
        label: 'Send',
        kind: 'primary',
        onClick: async (close) => {
          const text = ta.value.trim();
          if (!text && !files.length) return;
          close();
          let sent = 0;
          for (const userId of list.members) {
            try {
              const { chat } = await api.post('/api/chats/direct', { userId });
              S.chats.set(chat.id, chat);
              if (text) await A.sendText(chat.id, text);
              for (const f of files) await A.sendFile(chat.id, f, { kind: kindFor(f) });
              sent++;
            } catch {
              // reported by the send itself
            }
          }
          toast(`Broadcast sent to ${sent} of ${list.members.length}`);
        },
      },
    ],
  });
}

function broadcastsPanel() {
  const lists = broadcasts();
  return panelShell('Broadcast lists', [
    h('div', { class: 'row short', onclick: newBroadcast }, h('div', { class: 'row-icon accent' }, icon('add')), h('div', { class: 'body' }, 'New broadcast list')),
    lists.map((b) => h('div', { class: 'row', onclick: () => broadcastDialog(b) }, h('div', { class: 'row-icon accent' }, icon('broadcast')), h('div', { class: 'body' }, h('div', { class: 'name ellipsis' }, b.name), h('div', { class: 'preview ellipsis' }, `${b.members.length} recipient${b.members.length === 1 ? '' : 's'}: ${b.members.map(A.nameOf).join(', ')}`)))),
    h('div', { class: 'empty small' }, 'Send one message to several devices at once. Each one gets it as a normal private message. Broadcast lists are saved in this browser.'),
  ]);
}

function panelNodes() {
  switch (S.panel) {
    case 'new-chat':
      return newChatPanel();
    case 'new-group':
      return newGroupPanel();
    case 'archived':
      return archivedPanel();
    case 'starred':
      return starredPanel();
    case 'settings':
      return settingsPanel();
    case 'profile':
      return profilePanel();
    case 'privacy':
      return privacyPanel();
    case 'chat-settings':
      return chatSettingsPanel();
    case 'notifications':
      return notificationsPanel();
    case 'devices':
      return devicesPanel();
    case 'help':
      return helpPanel();
    case 'broadcasts':
      return broadcastsPanel();
    case 'channels':
      return channelsPanel();
    case 'community':
      return communityPanel();
    default:
      S.panel = null;
      return null;
  }
}

function renderSidebar() {
  if (panelKey !== S.panel) {
    panelKey = S.panel;
    panelSearchEl = null;
    if (S.panel !== 'devices') devicesCache = null;
    if (S.panel !== 'new-group') ng = null;
    if (S.panel !== 'channels') directory = null;
  }
  let nodes = S.panel ? panelNodes() : null;
  if (!nodes) {
    let body;
    if (S.view === 'status') body = updatesTab();
    else if (S.view === 'communities') body = communitiesTab();
    else if (S.view === 'calls') body = callsTab();
    else body = chatsTab();
    nodes = [topbar(), tabs(), body];
  }
  reconcile(sidebarEl, nodes);
}

// ---------- shell ----------

function welcome() {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 64 64');
  svg.setAttribute('width', '96');
  svg.setAttribute('height', '96');
  const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
  use.setAttribute('href', '/icon.svg#mark');
  svg.append(use);
  return h(
    'div',
    { class: 'welcome' },
    svg,
    h('h1', null, 'Sphynx Chat'),
    h('p', null, 'Message, call and share files between the devices on your Sphynx network. Every device is its own contact.'),
    h('p', null, `Open ${address()} on your other devices to chat with them.`),
    h('div', { class: 'lock' }, icon('lock'), 'Your personal messages are end-to-end encrypted'),
  );
}

function update() {
  const chat = S.active ? S.chats.get(S.active) : null;
  if (S.active && !chat) S.active = null;
  root.classList.toggle('chat-open', !!chat);
  renderSidebar();
  reconcile(mainEl, [chat ? convEl : welcomeEl, chat && S.details ? detailsEl : null]);
  if (chat) renderConversation(convEl, chat);
  if (chat && S.details) renderDetails(detailsEl, chat);
  O.renderCalls();
  A.updateTitle();
}

function onKey(e) {
  if (e.key !== 'Escape' || document.getElementById('layer').children.length) return;
  if (S.details) S.details = null;
  else if (S.panel) S.panel = null;
  else return;
  render();
}

export function mount() {
  root = document.getElementById('app');
  sidebarEl = h('aside', { class: 'sidebar' });
  mainEl = h('section', { class: 'main' });
  convEl = h('div', { class: 'conversation' });
  detailsEl = h('aside', { class: 'details' });
  welcomeEl = welcome();
  root.replaceChildren(sidebarEl, mainEl);
  A.onRender(update);
  document.addEventListener('keydown', onKey);
  // Files dropped on the open chat go to the attach dialog.
  convEl.addEventListener('dragover', (e) => e.preventDefault());
  convEl.addEventListener('drop', (e) => {
    e.preventDefault();
    const chat = S.chats.get(S.active);
    const files = [...(e.dataTransfer?.files || [])];
    if (chat && files.length && !canSend(chat)) attachDialog(chat, files);
  });
  update();
  const invite = /^\/invite\/([A-Za-z0-9_-]{6,64})$/.exec(location.pathname);
  if (invite) O.handleInvite(invite[1]);
}
