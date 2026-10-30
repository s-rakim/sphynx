// Sphynx radio mode: a radio station is a contact whose one "browser" is
// the bridge inside the server. Messages are sealed and opened there in Go,
// so this checks the Go envelope code against the browsers' own.
import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { chatAad, newMessageId } from '../web/crypto.js';
import { connectUser, startServer } from './helpers.js';

const KEY = 'meshtastic:!a1b2c3d4';
let server;

before(async () => {
  server = await startServer({ CHAT_DEV_RADIO: `Field radio=${KEY}`, CHAT_DEV_RADIO_TO: 'ana@dev' });
});

after(async () => {
  await server?.stop();
});

async function dev(method, path, body) {
  const res = await fetch(server.base + path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, ...(await res.json()) };
}

test('radio messages reach browsers, and replies go out over the radio', async () => {
  const ana = await connectUser(server.base, 'ana');
  await ana.socket();
  const { users } = await ana.call('GET', '/api/users');
  const radio = users.find((u) => u.radio === 'meshtastic');
  assert.equal(radio.name, 'Field radio');
  assert.equal(radio.joined, true);
  assert.equal(radio.onNetwork, true);

  // Sealed by the server, opened with the browser's code.
  assert.deepEqual((await dev('POST', '/dev/radio/receive', { key: KEY, text: 'hello from the hill' })).names, ['Ana']);
  const { message: m } = await ana.waitFor((e) => e.type === 'message' && e.message.senderId === radio.id);
  assert.deepEqual(await ana.ring.open(m.payload, chatAad(m.chatId, m.id), radio.id), { type: 'text', text: 'hello from the hill' });

  // Sealed by the browser, opened by the server and sent over the radio.
  const devices = (await ana.call('GET', `/api/chats/${m.chatId}/devices`)).devices;
  assert.ok(devices.some((d) => d.userId === radio.id));
  const id = newMessageId();
  const payload = await ana.ring.seal({ type: 'text', text: 'on my way' }, chatAad(m.chatId, id), devices);
  const sent = await ana.call('POST', `/api/chats/${m.chatId}/messages`, { id, payload });
  await ana.waitFor((e) => e.type === 'receipt' && e.member.userId === radio.id && e.member.deliveredSeq >= sent.message.seq);
  assert.deepEqual((await dev('GET', '/dev/radio/sent')).sent, [{ key: KEY, text: 'Ana: on my way' }]);

  // Not acknowledged: a note in the chat says so.
  const id2 = newMessageId();
  const payload2 = await ana.ring.seal({ type: 'text', text: 'fail please' }, chatAad(m.chatId, id2), devices);
  await ana.call('POST', `/api/chats/${m.chatId}/messages`, { id: id2, payload: payload2 });
  const note = await ana.waitFor((e) => e.type === 'message' && e.message.kind === 'system' && e.message.chatId === m.chatId);
  assert.deepEqual(JSON.parse(note.message.payload), { event: 'radio-failed', error: 'no acknowledgement' });
});

test('radio contacts stay out of groups, and strangers stay out', async () => {
  const ben = await connectUser(server.base, 'ben');
  const radio = (await ben.call('GET', '/api/users')).users.find((u) => u.radio);
  await assert.rejects(ben.call('POST', '/api/chats/group', { name: 'Trip', members: [radio.id] }), { status: 400 });
  assert.equal((await dev('POST', '/dev/radio/receive', { key: 'aprs:N0CALL', text: 'hi' })).status, 409);
  // Status updates aren't encrypted for the bridge.
  const { devices } = await ben.call('GET', '/api/status/devices');
  assert.ok(devices.every((d) => d.userId !== radio.id));
});
